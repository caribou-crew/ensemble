package server_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/caribou-crew/ensemble/ensemble/config"
)

// TestReconcileEndpointAppliesOnlyChangedUnits drives POST /api/reconcile
// over real HTTP (the same path `ensemble up`, run a second time against an
// already-up stack, uses instead of today's plain "already running"
// no-op): posting a config whose only difference is an added service must
// start exactly that service and report every other unit unchanged.
func TestReconcileEndpointAppliesOnlyChangedUnits(t *testing.T) {
	e := newTestEnv(t)

	newCfg := *e.cfg
	newCfg.Services = map[string]config.Service{}
	for k, v := range e.cfg.Services {
		newCfg.Services[k] = v
	}
	newCfg.Services["extra"] = config.Service{Run: "sleep 30"}

	body, err := json.Marshal(newCfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	resp, respBody := e.do(t, http.MethodPost, "/api/reconcile", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/reconcile: status %d, body %s", resp.StatusCode, respBody)
	}

	var result struct {
		Actions []struct {
			Kind   string `json:"kind"`
			Name   string `json:"name"`
			Action string `json:"action"`
		} `json:"actions"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		t.Fatalf("unmarshal response: %v (body: %s)", err, respBody)
	}

	found := map[string]string{}
	for _, a := range result.Actions {
		found[a.Kind+"/"+a.Name] = a.Action
	}
	if got := found["service/extra"]; got != "started" {
		t.Errorf("service/extra action = %q, want started", got)
	}
	if got := found["service/svc"]; got != "unchanged" {
		t.Errorf("service/svc action = %q, want unchanged", got)
	}

	st, ok := e.orch.Service("extra")
	if !ok || st.PID == 0 {
		t.Errorf("service extra must be running after reconcile, got %+v (ok=%v)", st, ok)
	}
}
