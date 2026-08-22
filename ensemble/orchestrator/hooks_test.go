package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/caribou-crew/ensemble/ensemble/config"
)

// Test: a service's on_healthy hook runs once its health gate passes, in
// its own Dir, with output landing in the service's own log.
func TestOnHealthyHookRunsAfterHealthGate(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "seeded")

	cfg := &config.Config{
		Dir: dir,
		Services: map[string]config.Service{
			"svc": {
				Dir:       dir,
				Run:       "sleep 30",
				OnHealthy: fmt.Sprintf("echo done > %s", marker),
			},
		},
	}
	o := newTestOrchestrator(t, cfg, Opts{})
	if err := o.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	defer o.Down()

	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("on_healthy hook did not run: %v", err)
	}
	st, _ := o.Service("svc")
	if st.Status != StatusHealthy {
		t.Fatalf("status = %v, want healthy", st.Status)
	}
	b, _ := os.ReadFile(filepath.Join(o.opts.LogDir, "svc.log"))
	if !strings.Contains(string(b), "on_healthy hook ok") {
		t.Errorf("service log lacks the on_healthy hook output:\n%s", b)
	}
}

// Test: a failing on_healthy hook fails the service (not silently reported
// healthy) and Up reports it, with the reason in both LastErr and the
// service log — the same shape as a failing Build.
func TestOnHealthyHookFailureFailsService(t *testing.T) {
	cfg := &config.Config{
		Dir: t.TempDir(),
		Services: map[string]config.Service{
			"svc": {Run: "sleep 30", OnHealthy: "echo 'seed: constraint violation' >&2; exit 1"},
		},
	}
	o := newTestOrchestrator(t, cfg, Opts{})
	err := o.Up(context.Background())
	defer o.Down()
	if err == nil || !strings.Contains(err.Error(), "constraint violation") {
		t.Fatalf("Up err = %v", err)
	}
	st, _ := o.Service("svc")
	if st.Status != StatusFailed || !strings.Contains(st.LastErr, "constraint violation") {
		t.Fatalf("state = %+v", st)
	}
	b, _ := os.ReadFile(filepath.Join(o.opts.LogDir, "svc.log"))
	if !strings.Contains(string(b), "=== on_healthy hook failed") {
		t.Errorf("service log lacks the on_healthy hook failure:\n%s", b)
	}
}

// Test: on_ready's plain shell command runs once Up has confirmed every
// active node healthy — the "postinstall step" half of on_ready.
func TestOnReadyRunPostinstallStep(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "postinstalled")

	cfg := &config.Config{
		Dir: dir,
		Services: map[string]config.Service{
			"svc": {Run: "sleep 30"},
		},
		OnReady: config.OnReady{Run: fmt.Sprintf("echo done > %s", marker)},
	}
	o := newTestOrchestrator(t, cfg, Opts{})
	if err := o.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	defer o.Down()

	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("on_ready.run did not run: %v", err)
	}
}

// Test: on_ready.seeds runs the named seeds, in declared order, through
// the same SQLRunner a manual seed uses — the "seed scripts" half of
// on_ready.
func TestOnReadySeedsRunInDeclaredOrder(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"a.sql", "b.sql"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("-- seed"), 0o644); err != nil {
			t.Fatalf("write %s: %v", f, err)
		}
	}

	cfg := &config.Config{
		Dir: dir,
		Services: map[string]config.Service{
			"svc": {Run: "sleep 30"},
		},
		Seeds: map[string]config.Seed{
			"first":  {SQL: []config.SeedSQL{{Database: "primary", File: "a.sql"}}},
			"second": {SQL: []config.SeedSQL{{Database: "primary", File: "b.sql"}}},
		},
		OnReady: config.OnReady{Seeds: []string{"first", "second"}},
	}
	o := newTestOrchestrator(t, cfg, Opts{})
	runner := &fakeSQLRunner{}
	o.SQLRunner = runner

	if err := o.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	defer o.Down()

	want := []string{"primary::" + filepath.Join(dir, "a.sql"), "primary::" + filepath.Join(dir, "b.sql")}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("SQLRunner calls = %v, want %v", runner.calls, want)
	}
}

// Test: on_ready never runs if any node failed to start — a partial stack
// isn't "ready" in the sense a seed script or postinstall step cares
// about.
func TestOnReadySkippedWhenANodeFails(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "postinstalled")

	cfg := &config.Config{
		Dir: dir,
		Services: map[string]config.Service{
			"good": {Run: "sleep 30"},
			"bad":  {}, // neither run nor docker set: fails to start
		},
		OnReady: config.OnReady{Run: fmt.Sprintf("echo done > %s", marker)},
	}
	o := newTestOrchestrator(t, cfg, Opts{})
	err := o.Up(context.Background())
	defer o.Down()
	if err == nil {
		t.Fatal("expected Up to report the bad service's failure")
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("on_ready.run should not have run — one node failed")
	}
}
