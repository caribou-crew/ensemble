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

func newLaneEnv(t *testing.T) *testEnv {
	t.Helper()
	cfg := &config.Config{
		Dir: t.TempDir(),
		Services: map[string]config.Service{
			"shared": {Run: "sleep 30"},
			"a1":     {Run: "sleep 30", Profile: "lane1"},
			"b1":     {Run: "sleep 30", Profile: "lane2"},
		},
	}
	rec := proxy.NewRecorder(proxy.RecorderOpts{Ring: 16})
	px := proxy.New(rec)
	orch := orchestrator.New(cfg, px, orchestrator.Opts{LogDir: t.TempDir(), Profiles: []string{"lane1"}})
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

func TestProfilesEndpoints(t *testing.T) {
	e := newLaneEnv(t)
	decode := func(body []byte) orchestrator.ProfilesState {
		var st orchestrator.ProfilesState
		if err := json.Unmarshal(body, &st); err != nil {
			t.Fatalf("decode %s: %v", body, err)
		}
		return st
	}

	_, body := e.get(t, "/api/profiles")
	st := decode(body)
	if strings.Join(st.Active, ",") != "lane1" || len(st.Profiles) != 2 || st.Profiles[1].Name != "lane2" || st.Profiles[1].Active {
		t.Fatalf("initial = %+v", st)
	}

	resp, body := e.post(t, "/api/profiles/lane2/up", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("up: %d %s", resp.StatusCode, body)
	}
	if st = decode(body); strings.Join(st.Active, ",") != "lane1,lane2" {
		t.Fatalf("after up = %+v", st)
	}
	if s, _ := e.orch.Service("b1"); s.Status != orchestrator.StatusHealthy {
		t.Fatalf("b1 = %+v", s)
	}

	resp, body = e.post(t, "/api/profiles/lane1/down", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("down: %d %s", resp.StatusCode, body)
	}
	if st = decode(body); strings.Join(st.Active, ",") != "lane2" {
		t.Fatalf("after down = %+v", st)
	}
	if s, _ := e.orch.Service("a1"); s.Status != orchestrator.StatusStopped {
		t.Fatalf("a1 = %+v", s)
	}
	if s, _ := e.orch.Service("shared"); s.Status != orchestrator.StatusHealthy {
		t.Fatalf("shared = %+v", s)
	}

	if resp, _ := e.post(t, "/api/profiles/nope/up", nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown up: %d", resp.StatusCode)
	}
	if resp, _ := e.post(t, "/api/profiles/nope/down", nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown down: %d", resp.StatusCode)
	}
}
