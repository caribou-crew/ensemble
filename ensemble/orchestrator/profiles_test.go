package orchestrator

import (
	"context"
	"strings"
	"testing"

	"github.com/caribou-crew/ensemble/ensemble/config"
)

// laneConfig: `shared` is always-on, `a1` is lane1, `b1` is lane2 and
// `b2` joins lane2 via the top-level group; `both` is in lane1 AND lane2.
func laneConfig(t *testing.T) *config.Config {
	t.Helper()
	return &config.Config{
		Dir: t.TempDir(),
		Services: map[string]config.Service{
			"shared": {Run: "sleep 30"},
			"a1":     {Run: "sleep 30", Profile: "lane1", DependsOn: []string{"shared"}},
			"b1":     {Run: "sleep 30", Profile: "lane2", DependsOn: []string{"shared"}},
			"b2":     {Run: "sleep 30", DependsOn: []string{"b1"}},
			"both":   {Run: "sleep 30", Profile: "lane1"},
		},
		Profiles: map[string][]string{"lane2": {"b2", "both"}},
	}
}

func pidOf(t *testing.T, o *Orchestrator, name string) int {
	t.Helper()
	st, ok := o.Service(name)
	if !ok {
		return 0
	}
	return st.PID
}

func TestUpAndDownProfiles(t *testing.T) {
	o := newTestOrchestrator(t, laneConfig(t), Opts{Profiles: []string{"lane1"}})
	if err := o.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	defer o.Down()

	if !o.running("shared") || !o.running("a1") || !o.running("both") || o.running("b1") || o.running("b2") {
		t.Fatalf("after Up lane1: states %+v", o.States())
	}
	sharedPID, bothPID := pidOf(t, o, "shared"), pidOf(t, o, "both")

	st := o.Profiles()
	if strings.Join(st.Active, ",") != "lane1" || len(st.Profiles) != 2 || st.Profiles[1].Name != "lane2" || st.Profiles[1].Active || strings.Join(st.Profiles[1].Services, ",") != "b1,b2,both" {
		t.Fatalf("Profiles() = %+v", st)
	}

	// Second lane joins: only b1 and b2 start; shared/both keep their PIDs.
	if err := o.UpProfiles(context.Background(), []string{"lane2"}); err != nil {
		t.Fatalf("UpProfiles lane2: %v", err)
	}
	if !o.running("b1") || !o.running("b2") {
		t.Fatalf("lane2 not started: %+v", o.States())
	}
	if pidOf(t, o, "shared") != sharedPID || pidOf(t, o, "both") != bothPID {
		t.Errorf("shared/both restarted by UpProfiles")
	}
	if got := strings.Join(o.Profiles().Active, ","); got != "lane1,lane2" {
		t.Errorf("active = %q", got)
	}

	// Drop lane1: a1 stops; both survives (lane2 still names it); shared is always-on.
	if err := o.DownProfiles([]string{"lane1"}); err != nil {
		t.Fatalf("DownProfiles lane1: %v", err)
	}
	if o.running("a1") || !o.running("both") || !o.running("shared") || !o.running("b1") {
		t.Fatalf("after down lane1: %+v", o.States())
	}
	if st, _ := o.Service("a1"); st.Status != StatusStopped || st.PID != 0 {
		t.Errorf("a1 state = %+v", st)
	}
	if pidOf(t, o, "both") != bothPID {
		t.Errorf("both restarted on lane1 down")
	}

	// Drop lane2: b1, b2, both stop (dependents first); shared stays.
	if err := o.DownProfiles([]string{"lane2"}); err != nil {
		t.Fatalf("DownProfiles lane2: %v", err)
	}
	if o.running("b1") || o.running("b2") || o.running("both") || !o.running("shared") {
		t.Fatalf("after down lane2: %+v", o.States())
	}
	if pidOf(t, o, "shared") != sharedPID {
		t.Errorf("shared restarted")
	}
	if err := o.Restart(context.Background(), "b1"); err == nil || !strings.Contains(err.Error(), "not an active service") {
		t.Errorf("restart of inactive lane service: %v", err)
	}

	// Re-activate lane1: a1 and both come back.
	if err := o.UpProfiles(context.Background(), []string{"lane1"}); err != nil {
		t.Fatalf("UpProfiles lane1 again: %v", err)
	}
	if !o.running("a1") || !o.running("both") {
		t.Fatalf("lane1 not restarted: %+v", o.States())
	}

	if err := o.UpProfiles(context.Background(), []string{"nope"}); err == nil || !strings.Contains(err.Error(), `"nope"`) {
		t.Errorf("unknown profile: %v", err)
	}
	if err := o.DownProfiles([]string{"nope"}); err == nil {
		t.Errorf("unknown profile down should error")
	}
}

// A lane service with a proxy port brought down and up again must reuse
// its listener rather than rebinding (which would fail: address in use).
func TestUpProfilesReusesProxyListener(t *testing.T) {
	cfg := laneConfig(t)
	svc := cfg.Services["a1"]
	svc.Port = freePort(t)
	svc.Proxy = freePort(t)
	cfg.Services["a1"] = svc
	o := newTestOrchestrator(t, cfg, Opts{Profiles: []string{"lane1"}})
	// No health gate can pass on a `sleep` with a port, so skip the gate
	// by giving the port only to the proxy wiring: run without Port.
	svc.Port = 0
	cfg.Services["a1"] = svc
	if err := o.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	defer o.Down()
	if err := o.DownProfiles([]string{"lane1"}); err != nil {
		t.Fatal(err)
	}
	if err := o.UpProfiles(context.Background(), []string{"lane1"}); err != nil {
		t.Fatalf("UpProfiles after down must not rebind the listener: %v", err)
	}
}
