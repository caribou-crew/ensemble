package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/caribou-crew/ensemble/ensemble/config"
)

// variantConfig declares one service with two variants. Each variant's
// build appends to builds-<v> and its run writes marker=<v> before
// sleeping, so tests can see which variant was built/started.
func variantConfig(t *testing.T) (*config.Config, string) {
	t.Helper()
	dir := t.TempDir()
	// Each variant gets its own dir (as a stub and a monolith would); the
	// marker lands in the shared parent so the run never dirties the
	// variant's own tree after its build stamp.
	mk := func(v string) config.Variant {
		vdir := filepath.Join(dir, v)
		if err := os.MkdirAll(vdir, 0o755); err != nil {
			t.Fatal(err)
		}
		return config.Variant{
			Dir:   vdir,
			Watch: []string{"*.src"}, // nothing matches: a fresh stamp means not stale
			Build: "echo x >> builds-" + v,
			Run:   "sh -c 'echo " + v + " > ../marker; sleep 30'",
		}
	}
	cfg := &config.Config{
		Dir: dir,
		Services: map[string]config.Service{
			"mono":  {Default: "stub", Variants: map[string]config.Variant{"stub": mk("stub"), "real": mk("real")}},
			"plain": {Run: "sleep 30"},
		},
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "port is 0") {
		// Port 0 is only tolerated here because no health gate needs it.
		t.Logf("validate: %v", err)
	}
	return cfg, dir
}

// waitMarker polls dir/marker until it reads want — the run command writes
// it asynchronously after start and there is no health gate to wait on.
func waitMarker(t *testing.T, dir, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if got := readFile(t, filepath.Join(dir, "marker")); got == want {
			return
		} else if time.Now().After(deadline) {
			t.Fatalf("marker = %q, want %q", got, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func lines(t *testing.T, path string) int {
	s := readFile(t, path)
	if s == "" {
		return 0
	}
	return len(strings.Split(s, "\n"))
}

func TestUpStartsDefaultVariantAndSwitches(t *testing.T) {
	cfg, dir := variantConfig(t)
	o := newTestOrchestrator(t, cfg, Opts{})
	if err := o.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	defer o.Down()

	waitMarker(t, dir, "stub")
	st, _ := o.Service("mono")
	if st.Variant != "stub" || st.Status != StatusHealthy {
		t.Fatalf("state after Up = %+v", st)
	}
	if cur, avail := o.Variant("mono"); cur != "stub" || strings.Join(avail, ",") != "real,stub" {
		t.Errorf("Variant() = %q %v", cur, avail)
	}
	if cur, avail := o.Variant("plain"); cur != "" || avail != nil {
		t.Errorf("Variant(plain) = %q %v", cur, avail)
	}
	if _, err := os.Stat(filepath.Join(o.opts.LogDir, "mono.stub.buildstamp")); err != nil {
		t.Errorf("per-variant build stamp missing: %v", err)
	}
	stubPID := st.PID

	if err := o.SetVariant(context.Background(), "mono", "real"); err != nil {
		t.Fatalf("SetVariant real: %v", err)
	}
	waitMarker(t, dir, "real")
	st, _ = o.Service("mono")
	if st.Variant != "real" || st.PID == stubPID || st.Status != StatusHealthy {
		t.Fatalf("state after switch = %+v (stub pid %d)", st, stubPID)
	}
	if n := lines(t, filepath.Join(dir, "real", "builds-real")); n != 1 {
		t.Errorf("real built %d times, want 1 (stub's stamp must not cover it)", n)
	}

	// Restart keeps the variant.
	if err := o.Restart(context.Background(), "mono"); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if st, _ = o.Service("mono"); st.Variant != "real" {
		t.Fatalf("after Restart: state %+v", st)
	}
	waitMarker(t, dir, "real")

	// Switching back does not rebuild stub: its stamp is fresh.
	if err := o.SetVariant(context.Background(), "mono", "stub"); err != nil {
		t.Fatalf("SetVariant stub: %v", err)
	}
	if n := lines(t, filepath.Join(dir, "stub", "builds-stub")); n != 1 {
		t.Errorf("stub built %d times, want 1", n)
	}

	// Unknown variant: error, nothing stopped.
	st, _ = o.Service("mono")
	err := o.SetVariant(context.Background(), "mono", "prod")
	if err == nil || !strings.Contains(err.Error(), `"prod"`) {
		t.Fatalf("unknown variant err = %v", err)
	}
	if after, _ := o.Service("mono"); after.PID != st.PID || after.Status != StatusHealthy {
		t.Errorf("unknown variant disturbed the service: %+v -> %+v", st, after)
	}
	if err := o.SetVariant(context.Background(), "plain", "x"); err == nil || !strings.Contains(err.Error(), "no variants") {
		t.Errorf("plain: %v", err)
	}
}

func TestSetVariantWhileStoppedOnlyRecords(t *testing.T) {
	cfg, dir := variantConfig(t)
	o := newTestOrchestrator(t, cfg, Opts{})
	if err := o.SetVariant(context.Background(), "mono", "real"); err != nil {
		t.Fatalf("SetVariant before Up: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "marker")); err == nil {
		t.Fatal("nothing should have started")
	}
	if err := o.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	defer o.Down()
	waitMarker(t, dir, "real")
}

func TestOptsVariantsOverrideDefault(t *testing.T) {
	cfg, dir := variantConfig(t)
	o := newTestOrchestrator(t, cfg, Opts{Variants: map[string]string{"mono": "real"}})
	if err := o.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	defer o.Down()
	waitMarker(t, dir, "real")
}
