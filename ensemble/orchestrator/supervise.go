package orchestrator

// Supervision: a process that exits after a successful start becomes a
// visible state (exited/crashed) instead of staying "healthy" forever.
// Native processes are reaped the moment Wait returns (noteNativeExit,
// hooked in via startNativeProcess's onExit); docker-placed nodes have no
// Wait to hook, so a background poll notices a container that stopped or
// vanished. Both paths record exit code/signal/time, tail the service log
// into LastErr for a crash, and land a control-plane hop on the recorder
// so the SSE traffic stream carries the status change live. Nothing here
// ever restarts anything — restart stays a deliberate action.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/caribou-crew/ensemble/core/trace"
)

// exitTailBytes bounds how much of the service log a crash's LastErr
// carries — same budget as a failed shell step's error tail.
const exitTailBytes = shellStepTailBytes

// dockerSupervisionInterval is the docker supervision poll cadence. A var,
// not a const, so tests can shorten it instead of waiting out real seconds.
var dockerSupervisionInterval = 5 * time.Second

// noteNativeExit is startNativeProcess's onExit callback: cmd's Wait has
// returned. If the orchestrator still tracks this exact cmd for name and
// isn't itself tearing it down (see the stopping field comment), the exit
// was the process's own doing — record it. Any other case (operator
// kill, an instance already replaced by Restart/Flip) is someone else's
// transition to report.
func (o *Orchestrator) noteNativeExit(name string, cmd *exec.Cmd, logPath string) {
	o.mu.Lock()
	tracked := o.procs[name] == cmd
	deliberate := o.stopping[name]
	if tracked && !deliberate {
		delete(o.procs, name)
	}
	o.mu.Unlock()
	if !tracked || deliberate {
		return
	}

	code := -1
	signal := ""
	if ps := cmd.ProcessState; ps != nil {
		code = ps.ExitCode()
		signal = exitSignal(ps)
	}
	o.recordExit(name, code, signal, logTail(logPath, exitTailBytes))
}

// recordExit moves name to exited (clean zero exit) or crashed (anything
// else), stamps the exit details, and emits the status event. code < 0
// means "no exit code" (died to a signal, or the state was unreadable).
func (o *Orchestrator) recordExit(name string, code int, signal, tail string) {
	status := StatusCrashed
	if code == 0 && signal == "" {
		status = StatusExited
	}
	label := exitLabel(code, signal)
	tail = strings.TrimSpace(tail)

	o.setState(name, func(s *ServiceState) {
		s.Status = status
		s.PID = 0
		if code >= 0 {
			c := code
			s.ExitCode = &c
		} else {
			s.ExitCode = nil
		}
		s.Signal = signal
		s.ExitedAt = time.Now()
		if status == StatusCrashed {
			// The log tail is the closest thing to a reason the process
			// left behind; fall back to the bare exit label when the log
			// has nothing.
			if tail != "" {
				s.LastErr = tail
			} else {
				s.LastErr = label
			}
		} else {
			s.LastErr = ""
		}
	})
	o.logf("orchestrator: %s: process %s (%s)", name, status, label)

	detail := ""
	if status == StatusCrashed {
		detail = label
	}
	o.emitStatusEvent(name, status, detail)
}

// exitLabel renders how a process ended for logs and errors: "exit 1",
// "signal killed", or "unknown exit" when neither could be read.
func exitLabel(code int, signal string) string {
	if signal != "" {
		return "signal " + signal
	}
	if code < 0 {
		return "unknown exit"
	}
	return fmt.Sprintf("exit %d", code)
}

// emitStatusEvent lands a control-plane hop in the shared Recorder — the
// same convention server.withAnnotation uses for mutations. The recorder's
// ring feeds GET /api/traffic/stream, the SSE channel every live client
// (dashboard, TUI) is already subscribed to, so this is the "status event
// on the SSE stream" without inventing a second event bus. detail, when
// non-empty, is recorded as the hop's Err so a crash surfaces as an error
// hop in every traffic view.
func (o *Orchestrator) emitStatusEvent(name string, status Status, detail string) {
	if o.Rec == nil {
		return
	}
	o.Rec.Record(trace.Hop{
		To:     "ensemble-control",
		Method: "EVENT",
		Path:   fmt.Sprintf("/services/%s/status/%s", name, status),
		T:      trace.Timings{Start: time.Now()},
		Err:    detail,
	})
}

// logTail returns up to the last limit bytes of the file at path, "" when
// the file can't be read (not written yet, rotated away mid-read). Read
// from the file rather than teed through the process's pipes — see the
// startNativeProcess onExit comment for why a pipe is off the table.
func logTail(path string, limit int) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return ""
	}
	offset := max(info.Size()-int64(limit), 0)
	buf := make([]byte, info.Size()-offset)
	n, err := f.ReadAt(buf, offset)
	if n == 0 && err != nil {
		return ""
	}
	return string(buf[:n])
}

// --- docker supervision ---

// beginSupervision starts the background docker supervision loop — a
// no-op when one is already running (a second Up on the same Orchestrator
// must not leak a second loop). The loop itself is cheap when nothing is
// docker-placed: a pass over an empty dockerNodes map does no docker calls.
func (o *Orchestrator) beginSupervision() {
	o.mu.Lock()
	if o.superviseCancel != nil {
		o.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	o.superviseCancel = cancel
	done := make(chan struct{})
	o.superviseDone = done
	o.mu.Unlock()

	go o.runSupervisionLoop(ctx, done)
}

// runSupervisionLoop polls every tracked docker node's container each
// dockerSupervisionInterval until ctx is cancelled (by Down — see
// stopSupervision).
func (o *Orchestrator) runSupervisionLoop(ctx context.Context, done chan struct{}) {
	defer close(done)
	ticker := time.NewTicker(dockerSupervisionInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			o.runDockerSupervisionPass(ctx)
		}
	}
}

// stopSupervision cancels the docker supervision loop, if running, and
// waits for its goroutine to actually return — same contract as
// stopFreshness, and for the same reason: no state mutation may land after
// Down considers teardown complete.
func (o *Orchestrator) stopSupervision() {
	o.mu.Lock()
	cancel := o.superviseCancel
	done := o.superviseDone
	o.superviseCancel = nil
	o.superviseDone = nil
	o.mu.Unlock()

	if cancel == nil {
		return
	}
	cancel()
	<-done
}

// runDockerSupervisionPass checks every currently tracked docker node once.
func (o *Orchestrator) runDockerSupervisionPass(ctx context.Context) {
	o.mu.Lock()
	names := make([]string, 0, len(o.dockerNodes))
	for name := range o.dockerNodes {
		names = append(names, name)
	}
	o.mu.Unlock()
	sort.Strings(names)
	for _, name := range names {
		o.checkDockerNode(ctx, name)
	}
}

// checkDockerNode is the docker half of exit detection: a node the
// orchestrator believes is up whose container is stopped or gone entirely
// reaches the same exited/crashed states a native process's reaper
// produces. The container (when it still exists) is left in place — it
// stays trackable, so Down/Restart still remove it — which is why the
// status gate below matters: without it every later pass would re-report
// the same stopped container.
func (o *Orchestrator) checkDockerNode(ctx context.Context, name string) {
	// Only a node believed up can crash out from under us. Starting nodes
	// are the health gate's to judge; already-exited/crashed/stopped ones
	// have had their transition reported.
	if st, ok := o.Service(name); !ok || (st.Status != StatusHealthy && st.Status != StatusUnhealthy) {
		return
	}
	exists, running, exitCode, err := o.dockerState(ctx, name)
	if err != nil || running {
		// A daemon hiccup is not a crash; running is the happy path.
		return
	}

	// Re-check tracking under the lock right before reporting: an operator
	// Stop/Restart racing this pass either flags stopping or has already
	// removed the node from dockerNodes by the time it holds the maps.
	o.mu.Lock()
	tracked := o.dockerNodes[name]
	deliberate := o.stopping[name]
	o.mu.Unlock()
	if !tracked || deliberate {
		return
	}

	if !exists {
		// Removed out from under us — no exit code to read. That is never
		// a clean shutdown story, so it reports as crashed.
		o.recordExit(name, -1, "", "container removed outside ensemble")
		return
	}
	// The service's own log file only carries build/hook output for a
	// docker placement — the process's stdout/stderr live with the daemon,
	// so the crash tail comes from `docker logs` (best-effort).
	tail := ""
	if exitCode != 0 {
		if out, logErr := dockerOutput(ctx, "logs", "--tail", "40", dockerContainerName(name)); logErr == nil {
			tail = string(out)
		}
	}
	o.recordExit(name, exitCode, "", tail)
}
