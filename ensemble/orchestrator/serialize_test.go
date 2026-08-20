package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/ensemble-dev/ensemble/ensemble/config"
)

// Task-review finding: Flip/Restart read o.procs/o.dockerNodes, act (kill,
// docker rm, start replacement) outside the lock, then re-lock only to
// mutate the maps. Two concurrent operations on the SAME service (e.g. Flip
// racing Restart) can both read the pre-op placement before either commits
// its teardown, then both start a replacement — leaving the maps with
// either both placements tracked (a live orphan the other op doesn't know
// about) or one clobbering the other's map entry (an untracked, still-live
// process/container that Down will never find).
//
// This test forces the interleaving by slowing down the kill/remove step
// (via the existing killGroup/removeDockerContainer test seams) so both
// goroutines' initial reads land before either commits its teardown, then
// asserts the maps end up consistent: exactly one of native/docker tracked,
// and (if native) the tracked PID is actually alive.
func TestConcurrentFlipRestartSerialized(t *testing.T) {
	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "docker-calls.log")
	writeFakeDocker(t, binDir, logPath)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cfg := &config.Config{
		Dir: t.TempDir(),
		Services: map[string]config.Service{
			"svc": {
				Run: "sleep 30",
				Docker: &config.DockerPlacement{
					Image: "svc:local",
					Ports: []string{"8023:8080"},
				},
			},
		},
	}
	o := newTestOrchestrator(t, cfg, Opts{})

	if err := o.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	defer o.Down()

	before, ok := o.Service("svc")
	if !ok || before.Placement != "native" || before.PID == 0 {
		t.Fatalf("expected native placement w/ PID after Up, got %+v (ok=%v)", before, ok)
	}

	// Widen the race window: delay the teardown step of both paths so both
	// goroutines' pre-op reads (hasProc/isDocker) land before either
	// commits its kill/remove and mutates the maps.
	origKill := o.killGroup
	o.killGroup = func(pid int, sig syscall.Signal) error {
		time.Sleep(150 * time.Millisecond)
		return origKill(pid, sig)
	}
	origRemove := o.removeDockerContainer
	o.removeDockerContainer = func(name string) error {
		time.Sleep(150 * time.Millisecond)
		return origRemove(name)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = o.Flip(context.Background(), "svc")
	}()
	go func() {
		defer wg.Done()
		_ = o.Restart(context.Background(), "svc")
	}()
	wg.Wait()

	o.mu.Lock()
	procCmd, hasProc := o.procs["svc"]
	isDocker := o.dockerNodes["svc"]
	o.mu.Unlock()

	if hasProc && isDocker {
		t.Fatalf("both native and docker placement tracked for svc after concurrent flip+restart — orphaned tracking state")
	}
	if !hasProc && !isDocker {
		t.Fatalf("neither placement tracked for svc after concurrent flip+restart — lost track of the service entirely")
	}

	after, ok := o.Service("svc")
	if !ok {
		t.Fatal("expected a state for svc after concurrent flip+restart")
	}

	if hasProc {
		if procCmd.Process == nil || !processAlive(procCmd.Process.Pid) {
			t.Fatalf("tracked native process for svc is not alive — orphaned dead process tracked as live")
		}
		if after.PID != procCmd.Process.Pid {
			t.Fatalf("state PID %d does not match tracked process PID %d", after.PID, procCmd.Process.Pid)
		}
	}

	// The original process must not be alive-but-untracked: either it's
	// still the tracked one (hasProc && after.PID == before.PID) or it was
	// properly killed during teardown.
	if before.PID != 0 && processAlive(before.PID) {
		if !hasProc || after.PID != before.PID {
			t.Fatalf("original native process %d is still alive but untracked after concurrent flip+restart — orphaned process", before.PID)
		}
	}
}
