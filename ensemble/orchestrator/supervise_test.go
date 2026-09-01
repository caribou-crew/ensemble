package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/caribou-crew/ensemble/core/proxy"
	"github.com/caribou-crew/ensemble/ensemble/config"
)

// waitForStatus polls until name reaches want, failing the test after a
// generous deadline — supervision transitions are asynchronous (a reaper
// goroutine, not the caller's stack), so every assertion on them polls.
func waitForStatus(t *testing.T, o *Orchestrator, name string, want Status) ServiceState {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		st, _ := o.Service(name)
		if st.Status == want {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("service %s never reached %s (last: %+v)", name, want, st)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func newSupervisedOrchestrator(t *testing.T, run string) (*Orchestrator, *proxy.Recorder) {
	t.Helper()
	cfg := &config.Config{
		Dir: t.TempDir(),
		Services: map[string]config.Service{
			"svc": {Run: run},
		},
	}
	rec := proxy.NewRecorder(proxy.RecorderOpts{Ring: 64})
	o := New(cfg, proxy.New(rec), Opts{LogDir: t.TempDir()})
	o.Rec = rec
	t.Cleanup(func() { o.Down() })
	return o, rec
}

// TestProcessCrashAfterStartIsDetected is the headline scenario: a healthy
// service whose process then exits non-zero moves to crashed — with the
// exit code, an exit time, the log tail in LastErr, and a status event on
// the recorder — and nothing restarts it.
func TestProcessCrashAfterStartIsDetected(t *testing.T) {
	o, rec := newSupervisedOrchestrator(t, "echo boom-reason; sleep 0.3; exit 3")
	if err := o.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	st, _ := o.Service("svc")
	if st.Status != StatusHealthy {
		t.Fatalf("expected healthy right after Up, got %+v", st)
	}

	st = waitForStatus(t, o, "svc", StatusCrashed)
	if st.ExitCode == nil || *st.ExitCode != 3 {
		t.Errorf("ExitCode = %v, want 3", st.ExitCode)
	}
	if st.ExitedAt.IsZero() {
		t.Error("ExitedAt not stamped")
	}
	if st.PID != 0 {
		t.Errorf("PID = %d, want 0 after exit", st.PID)
	}
	if !strings.Contains(st.LastErr, "boom-reason") {
		t.Errorf("LastErr should carry the log tail, got %q", st.LastErr)
	}

	found := false
	for _, h := range rec.Snapshot() {
		if h.To == "ensemble-control" && h.Path == "/services/svc/status/crashed" {
			found = true
		}
	}
	if !found {
		t.Error("expected a crashed status event hop on the recorder")
	}
}

// TestCleanExitIsExitedNotCrashed: exit 0 is a distinct, calmer state —
// no LastErr, but the exit code and time are still recorded.
func TestCleanExitIsExitedNotCrashed(t *testing.T) {
	o, _ := newSupervisedOrchestrator(t, "sleep 0.3; exit 0")
	if err := o.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	st := waitForStatus(t, o, "svc", StatusExited)
	if st.ExitCode == nil || *st.ExitCode != 0 {
		t.Errorf("ExitCode = %v, want 0", st.ExitCode)
	}
	if st.LastErr != "" {
		t.Errorf("LastErr = %q, want empty for a clean exit", st.LastErr)
	}
	if st.ExitedAt.IsZero() {
		t.Error("ExitedAt not stamped")
	}
}

// TestOperatorStopIsNotACrash guards the reaper's race gate: the
// orchestrator killing the process itself (Stop) must land on stopped,
// never crashed, even though Wait returns from a SIGKILL either way.
func TestOperatorStopIsNotACrash(t *testing.T) {
	o, _ := newSupervisedOrchestrator(t, "while true; do sleep 1; done")
	if err := o.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	st, _ := o.Service("svc")
	pid := st.PID

	if err := o.Stop("svc"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	// Wait until the process is genuinely gone, then give the reaper
	// goroutine time to (wrongly) act if it were going to.
	deadline := time.Now().Add(3 * time.Second)
	for processAlive(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("process %d still alive after Stop", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(150 * time.Millisecond)

	st, _ = o.Service("svc")
	if st.Status != StatusStopped {
		t.Fatalf("status = %s, want stopped (operator stop must not read as a crash)", st.Status)
	}
	if st.ExitCode != nil || st.Signal != "" {
		t.Errorf("operator stop recorded exit details: %+v", st)
	}
}

// TestRestartClearsExitState: a crashed service restarted by the operator
// comes back with no stale exit residue.
func TestRestartClearsExitState(t *testing.T) {
	o, _ := newSupervisedOrchestrator(t, "sleep 0.3; test -f ready && exec sleep 30; exit 7")
	if err := o.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	waitForStatus(t, o, "svc", StatusCrashed)

	if err := os.WriteFile(filepath.Join(o.cfg.Dir, "ready"), nil, 0o600); err != nil {
		t.Fatalf("write ready flag: %v", err)
	}
	if err := o.Restart(context.Background(), "svc"); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	st, _ := o.Service("svc")
	if st.Status != StatusHealthy {
		t.Fatalf("status after restart = %s, want healthy", st.Status)
	}
	if st.ExitCode != nil || st.Signal != "" || !st.ExitedAt.IsZero() {
		t.Errorf("restart left stale exit state: %+v", st)
	}
}

// --- docker supervision (fake docker state — no daemon needed) ---

func newDockerSupervised(t *testing.T, status Status) *Orchestrator {
	t.Helper()
	cfg := &config.Config{Dir: t.TempDir(), Services: map[string]config.Service{}}
	o := New(cfg, proxy.New(proxy.NewRecorder(proxy.RecorderOpts{})), Opts{LogDir: t.TempDir()})
	o.mu.Lock()
	o.dockerNodes["svc"] = true
	o.mu.Unlock()
	o.setState("svc", func(s *ServiceState) {
		s.Status = status
		s.Placement = "docker"
	})
	return o
}

func TestDockerContainerGoneMarksCrashed(t *testing.T) {
	o := newDockerSupervised(t, StatusHealthy)
	o.dockerState = func(ctx context.Context, name string) (bool, bool, int, error) {
		return false, false, 0, nil // container removed entirely
	}
	o.runDockerSupervisionPass(context.Background())

	st, _ := o.Service("svc")
	if st.Status != StatusCrashed {
		t.Fatalf("status = %s, want crashed for a vanished container", st.Status)
	}
	if st.ExitCode != nil {
		t.Errorf("ExitCode = %v, want nil (no code readable for a removed container)", st.ExitCode)
	}
	if st.ExitedAt.IsZero() {
		t.Error("ExitedAt not stamped")
	}
}

func TestDockerContainerStoppedCleanMarksExited(t *testing.T) {
	o := newDockerSupervised(t, StatusHealthy)
	o.dockerState = func(ctx context.Context, name string) (bool, bool, int, error) {
		return true, false, 0, nil // exists, stopped, exit 0
	}
	o.runDockerSupervisionPass(context.Background())

	st, _ := o.Service("svc")
	if st.Status != StatusExited {
		t.Fatalf("status = %s, want exited for a clean container stop", st.Status)
	}
	if st.ExitCode == nil || *st.ExitCode != 0 {
		t.Errorf("ExitCode = %v, want 0", st.ExitCode)
	}
}

func TestDockerSupervisionSkipsDeliberateStops(t *testing.T) {
	o := newDockerSupervised(t, StatusHealthy)
	o.dockerState = func(ctx context.Context, name string) (bool, bool, int, error) {
		return false, false, 0, nil
	}
	o.mu.Lock()
	o.stopping["svc"] = true
	o.mu.Unlock()
	o.runDockerSupervisionPass(context.Background())

	st, _ := o.Service("svc")
	if st.Status != StatusHealthy {
		t.Fatalf("status = %s; a pass during an operator teardown must not report a crash", st.Status)
	}
}

// TestDockerSupervisionOnlyWatchesUpNodes: a node already exited/crashed/
// starting is not re-reported on every pass.
func TestDockerSupervisionOnlyWatchesUpNodes(t *testing.T) {
	o := newDockerSupervised(t, StatusStarting)
	calls := 0
	o.dockerState = func(ctx context.Context, name string) (bool, bool, int, error) {
		calls++
		return false, false, 0, nil
	}
	o.runDockerSupervisionPass(context.Background())
	if calls != 0 {
		t.Fatalf("supervision inspected a starting node (%d calls); that's the health gate's job", calls)
	}
	st, _ := o.Service("svc")
	if st.Status != StatusStarting {
		t.Fatalf("status = %s, want starting untouched", st.Status)
	}
}
