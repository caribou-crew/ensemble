package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/ensemble-dev/ensemble/core/proxy"
	"github.com/ensemble-dev/ensemble/ensemble/config"
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
