package orchestrator

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/caribou-crew/ensemble/core/proxy"
	"github.com/caribou-crew/ensemble/ensemble/config"
)

// TestUpRealSupervisionAndDown is brief test 4: a real long-running native
// process is started, its PID is alive, and Down kills the whole process
// group (the shell's "while true" loop child dies too).
func TestUpRealSupervisionAndDown(t *testing.T) {
	cfg := &config.Config{
		Dir: t.TempDir(),
		Services: map[string]config.Service{
			"svc": {Run: "while true; do sleep 1; done"},
		},
	}
	px := proxy.New(proxy.NewRecorder(proxy.RecorderOpts{}))
	o := New(cfg, px, Opts{LogDir: t.TempDir()})

	if err := o.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}

	st, ok := o.Service("svc")
	if !ok || st.PID == 0 {
		t.Fatalf("expected a running pid, got %+v (ok=%v)", st, ok)
	}
	if !processAlive(st.PID) {
		t.Fatalf("process %d not alive right after Up", st.PID)
	}

	if err := o.Down(); err != nil {
		t.Fatalf("Down: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for processAlive(st.PID) {
		if time.Now().After(deadline) {
			t.Fatalf("process group %d still alive after Down", st.PID)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestEnvSliceOverridesParentEnv guards against a plain
// append(os.Environ(), ...) regression: config env must win over an
// existing parent-process value for the same key, not just be present
// somewhere in the slice.
func TestEnvSliceOverridesParentEnv(t *testing.T) {
	t.Setenv("ENSEMBLE_PROC_TEST_VAR", "parent")

	out := envSlice(map[string]string{"ENSEMBLE_PROC_TEST_VAR": "config", "ENSEMBLE_PROC_TEST_NEW": "fresh"})

	got := map[string]string{}
	for _, kv := range out {
		if k, v, ok := strings.Cut(kv, "="); ok {
			if _, dup := got[k]; dup {
				t.Fatalf("duplicate key %q in envSlice output", k)
			}
			got[k] = v
		}
	}

	if got["ENSEMBLE_PROC_TEST_VAR"] != "config" {
		t.Errorf("ENSEMBLE_PROC_TEST_VAR: got %q, want config to win over parent", got["ENSEMBLE_PROC_TEST_VAR"])
	}
	if got["ENSEMBLE_PROC_TEST_NEW"] != "fresh" {
		t.Errorf("ENSEMBLE_PROC_TEST_NEW: got %q, want fresh", got["ENSEMBLE_PROC_TEST_NEW"])
	}
}

func TestResolveDir(t *testing.T) {
	base := "/tmp/base"
	if got := resolveDir(base, ""); got != base {
		t.Errorf("empty dir: got %q, want %q", got, base)
	}
	if got := resolveDir(base, "sub/dir"); got != "/tmp/base/sub/dir" {
		t.Errorf("relative dir: got %q", got)
	}
	if got := resolveDir(base, "/abs/dir"); got != "/abs/dir" {
		t.Errorf("absolute dir: got %q", got)
	}
	home, _ := homeDir()
	if got := resolveDir(base, "~/work"); got != home+"/work" {
		t.Errorf("tilde dir: got %q, want %q", got, home+"/work")
	}
}
