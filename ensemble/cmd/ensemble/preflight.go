package main

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/caribou-crew/ensemble/ensemble/config"
	"github.com/caribou-crew/ensemble/ensemble/orchestrator"
)

// dbContainerRunning asks whether a database port is held by the very
// container Up is about to adopt. Indirected through a var so preflight's
// tests can answer this without a docker daemon.
var dbContainerRunning = orchestrator.DatabaseContainerRunning

// preflightDockerTimeout bounds the container lookup. Preflight is the first
// thing `ensemble up` does, so an unbounded call against a wedged daemon would
// hang the command before it printed anything at all.
const preflightDockerTimeout = 5 * time.Second

// adoptableDatabasePort reports whether port's occupant is ensemble's own
// running container for database name — in which case it is not a conflict,
// it is the thing the orchestrator is about to reuse.
//
// A daemon that errors, times out, or isn't installed answers "no", so the
// port is reported as a conflict exactly as it was before. This check can only
// ever excuse a conflict, never invent one.
func adoptableDatabasePort(name string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), preflightDockerTimeout)
	defer cancel()
	running, err := dbContainerRunning(ctx, name)
	return err == nil && running
}

// runPreflightChecks runs every cfg.Preflight check in declared order,
// stopping at the first failure — see config.PreflightCheck. It runs
// before checkPortsFree (and everything else runUp does), so a missing
// dependency (docker/podman not running, a VPN down, an internal service
// unreachable) fails fast with a clear, config-authored message instead of
// surfacing confusingly deep inside the orchestrator's first docker/build
// call.
func runPreflightChecks(cfg *config.Config) error {
	for _, check := range cfg.Preflight {
		label := check.Name
		if label == "" {
			label = check.Run
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(check.EffectiveTimeoutS())*time.Second)
		path, args := preflightShell(check.Run)
		cmd := exec.CommandContext(ctx, path, args...)
		preflightConfigureKill(cmd)
		// A second-line defense behind preflightConfigureKill's whole-group
		// kill: caps how long CombinedOutput waits for the pipe-copying
		// goroutines to see EOF after Cancel runs, so a kill that somehow
		// doesn't fully land still can't hang the check past its timeout.
		cmd.WaitDelay = 2 * time.Second
		out, err := cmd.CombinedOutput()
		cancel()
		if err != nil {
			msg := check.Message
			if msg == "" {
				msg = strings.TrimSpace(string(out))
				if msg == "" {
					msg = err.Error()
				}
			}
			return fmt.Errorf("preflight %q failed: %s", label, msg)
		}
	}
	return nil
}

// checkPortsFree verifies every port the active stack (cfg filtered
// through activeProfiles, see Config.ActivePorts) would bind is actually
// free on this host, before ensemble starts anything. A conflict here is
// almost always either a stale ensemble run that didn't get torn down
// cleanly or an unrelated process squatting the port — either way, better
// to fail fast with a clear message than have some service silently miss
// its health gate later with no explanation. A port belonging to a
// profile-gated service that isn't active right now is never checked, so
// it being in use by something else is not a conflict.
func checkPortsFree(cfg *config.Config, activeProfiles []string) error {
	ports := cfg.ActivePorts(activeProfiles)
	nums := make([]int, 0, len(ports))
	for p := range ports {
		nums = append(nums, p)
	}
	sort.Ints(nums)

	// Which ports belong to a managed database, so a conflict on one can be
	// checked against the container Up would adopt. Built up front, but only
	// consulted for ports that actually conflict — the clean path never shells
	// out to docker.
	dbPort := make(map[int]string, len(cfg.Databases))
	for name, db := range cfg.Databases {
		if db.Port != 0 {
			dbPort[db.Port] = name
		}
	}

	var conflicts []string
	for _, port := range nums {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			// A database port held by ensemble's own running container is not
			// a conflict: the orchestrator reuses that container instead of
			// creating one, so refusing to start here would make adoption
			// unreachable — `up` would always fail on the second run.
			if name, ok := dbPort[port]; ok && adoptableDatabasePort(name) {
				continue
			}
			msg := fmt.Sprintf("port %d (%s) is already in use", port, ports[port])
			if who := identifyPort(port); who != "" {
				msg += " by " + who
			}
			conflicts = append(conflicts, msg)
			continue
		}
		ln.Close()
	}
	if len(conflicts) == 0 {
		return nil
	}
	return fmt.Errorf("port conflict, refusing to start:\n  %s", strings.Join(conflicts, "\n  "))
}

// preflightLsofTimeout bounds identifyPort's shell-out, for the same reason
// preflightDockerTimeout bounds the container lookup. lsof walks every open
// file on the machine, which means stat()ing every mounted filesystem: with an
// unresponsive network mount (an SMB share whose server stopped answering) it
// blocks indefinitely and never returns. identifyPort runs inside
// checkPortsFree, the first thing `ensemble up` does, so an unbounded call
// hangs the command before it prints anything — while producing nothing more
// than a diagnostic string the caller is explicitly fine without.
const preflightLsofTimeout = 2 * time.Second

// runLsof is indirected through a var so identifyPort's give-up behavior is
// testable without needing a genuinely wedged mount on the host running the
// tests.
var runLsof = func(ctx context.Context, port int) ([]byte, error) {
	// -b avoids the kernel calls that block on an unresponsive mount, and -w
	// suppresses the warnings -b then emits; -n and -P keep lsof from doing
	// DNS and /etc/services lookups. -b is the fix for the common case, but
	// it is not a guarantee, which is what the context and WaitDelay are for.
	cmd := exec.CommandContext(ctx, "lsof", "-nP", "-b", "-w",
		fmt.Sprintf("-iTCP:%d", port), "-sTCP:LISTEN")
	// A process wedged in uninterruptible I/O does not die on the context's
	// SIGKILL, and Output would then block in Wait on a pipe that never
	// closes — the very hang the context was meant to bound. WaitDelay caps
	// that second wait too.
	cmd.WaitDelay = time.Second
	return cmd.Output()
}

// identifyPort best-effort names the process listening on port, via lsof
// (present on macOS and most Linux dev boxes). Empty string on any
// failure — not installed, nothing found, unexpected output, or lsof taking
// longer than preflightLsofTimeout — this is a diagnostic nicety only, never
// load-bearing: checkPortsFree still reports the conflict either way.
func identifyPort(port int) string {
	ctx, cancel := context.WithTimeout(context.Background(), preflightLsofTimeout)
	defer cancel()
	out, err := runLsof(ctx, port)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		return "" // header only, or empty: lsof found no listener (race with our own probe)
	}
	fields := strings.Fields(lines[1])
	if len(fields) < 2 {
		return ""
	}
	return fmt.Sprintf("%s (pid %s)", fields[0], fields[1])
}
