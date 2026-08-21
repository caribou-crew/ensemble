package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/caribou-crew/ensemble/core/proxy"
	"github.com/caribou-crew/ensemble/ensemble/config"
	"github.com/caribou-crew/ensemble/ensemble/orchestrator"
	"github.com/caribou-crew/ensemble/ensemble/server"
)

func newVariantEnv(t *testing.T) *testEnv {
	t.Helper()
	dir := t.TempDir()
	cfg := &config.Config{
		Dir: dir,
		Services: map[string]config.Service{
			"mono": {Default: "stub", Variants: map[string]config.Variant{
				"stub": {Run: "sleep 30"},
				"real": {Run: "sleep 30"},
			}},
			"plain": {Run: "sleep 30"},
		},
	}
	rec := proxy.NewRecorder(proxy.RecorderOpts{Ring: 16})
	px := proxy.New(rec)
	orch := orchestrator.New(cfg, px, orchestrator.Opts{LogDir: t.TempDir()})
	if err := orch.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	t.Cleanup(func() { orch.Down() })
	sessions := proxy.NewSessionManager(px, rec, nil)
	t.Cleanup(sessions.Close)
	handler := server.New(server.Deps{Cfg: cfg, Orch: orch, Rec: rec, Lat: proxy.NewLatencyStore(nil), Sessions: sessions, Version: "test"})
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return &testEnv{ts: ts, rec: rec, orch: orch, cfg: cfg}
}

func TestServiceVariantEndpoint(t *testing.T) {
	e := newVariantEnv(t)

	// Topology advertises the choice only where it applies.
	_, body := e.get(t, "/api/topology")
	var topo server.TopologyResponse
	if err := json.Unmarshal(body, &topo); err != nil {
		t.Fatal(err)
	}
	for _, n := range topo.Nodes {
		switch n.Name {
		case "mono":
			if n.Variant != "stub" || strings.Join(n.Variants, ",") != "real,stub" {
				t.Errorf("mono node = %+v", n)
			}
		case "plain":
			if n.Variant != "" || n.Variants != nil {
				t.Errorf("plain node = %+v", n)
			}
		}
	}

	resp, body := e.post(t, "/api/services/mono/variant", []byte(`{"variant":"real"}`))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	var st orchestrator.ServiceState
	if err := json.Unmarshal(body, &st); err != nil || st.Variant != "real" || st.Status != orchestrator.StatusHealthy {
		t.Fatalf("state = %s (%v)", body, err)
	}

	if resp, body := e.post(t, "/api/services/mono/variant", []byte(`{"variant":"prod"}`)); resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "prod") {
		t.Errorf("unknown variant: %d %s", resp.StatusCode, body)
	}
	if resp, _ := e.post(t, "/api/services/plain/variant", []byte(`{"variant":"x"}`)); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("no variants: %d", resp.StatusCode)
	}
	if resp, _ := e.post(t, "/api/services/nope/variant", []byte(`{"variant":"x"}`)); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown service: %d", resp.StatusCode)
	}
	if resp, _ := e.post(t, "/api/services/mono/variant", []byte(`{`)); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bad json: %d", resp.StatusCode)
	}
	// Still real after the rejected requests.
	if st, _ := e.orch.Service("mono"); st.Variant != "real" {
		t.Errorf("variant after rejections = %q", st.Variant)
	}
}
