package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caribou-crew/ensemble/ensemble/config"
)

// Test: a service declaring both run and docker starts native by default
// (spec: "flips a native service to container placement"); Flip stops the
// native process and starts the container, leaving the proxy's ProxyPort
// untouched. Uses the fake `docker` on PATH from orchestrator_test.go.
func TestFlipNativeToDocker(t *testing.T) {
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
					Ports: []string{"8020:8080"},
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

	if err := o.Flip(context.Background(), "svc"); err != nil {
		t.Fatalf("Flip: %v", err)
	}

	after, ok := o.Service("svc")
	if !ok || after.Status != StatusHealthy || after.Placement != "docker" {
		t.Fatalf("state after flip = %+v (ok=%v), want healthy/docker", after, ok)
	}
	if after.ProxyPort != before.ProxyPort {
		t.Fatalf("ProxyPort changed across flip: %d -> %d", before.ProxyPort, after.ProxyPort)
	}
	if processAlive(before.PID) {
		t.Fatalf("native process %d still alive after flip to docker", before.PID)
	}

	calls, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read fake docker call log: %v", err)
	}
	if !strings.Contains(string(calls), "run -d --name ensemble-svc") {
		t.Errorf("docker run not recorded:\n%s", calls)
	}
}

// Test: flipping back (docker -> native) restarts the native process and
// removes the container, round-tripping placement.
func TestFlipDockerToNative(t *testing.T) {
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
					Ports: []string{"8021:8080"},
				},
			},
		},
	}
	o := newTestOrchestrator(t, cfg, Opts{})

	if err := o.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	defer o.Down()

	if err := o.Flip(context.Background(), "svc"); err != nil {
		t.Fatalf("first Flip (native->docker): %v", err)
	}
	mid, ok := o.Service("svc")
	if !ok || mid.Placement != "docker" {
		t.Fatalf("expected docker placement mid-test, got %+v (ok=%v)", mid, ok)
	}

	if err := o.Flip(context.Background(), "svc"); err != nil {
		t.Fatalf("second Flip (docker->native): %v", err)
	}
	after, ok := o.Service("svc")
	if !ok || after.Status != StatusHealthy || after.Placement != "native" || after.PID == 0 {
		t.Fatalf("state after second flip = %+v (ok=%v), want healthy/native w/ PID", after, ok)
	}

	calls, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read fake docker call log: %v", err)
	}
	if !strings.Contains(string(calls), "rm -f ensemble-svc") {
		t.Errorf("docker rm not recorded:\n%s", calls)
	}
}

// Test: a service declaring only one placement has nothing to flip to.
func TestFlipNoAlternatePlacementErrors(t *testing.T) {
	cfg := &config.Config{
		Dir: t.TempDir(),
		Services: map[string]config.Service{
			"svc": {Run: "sleep 30"},
		},
	}
	o := newTestOrchestrator(t, cfg, Opts{})

	if err := o.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	defer o.Down()

	err := o.Flip(context.Background(), "svc")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "svc") || !strings.Contains(err.Error(), "no alternate placement") {
		t.Fatalf("error = %v, want it to name svc and say 'no alternate placement'", err)
	}
}
