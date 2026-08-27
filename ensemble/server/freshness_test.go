package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/caribou-crew/ensemble/core/proxy"
	"github.com/caribou-crew/ensemble/ensemble/config"
	"github.com/caribou-crew/ensemble/ensemble/orchestrator"
	"github.com/caribou-crew/ensemble/ensemble/server"
)

// runGit is a minimal `git -C dir <args>` helper for this file's own
// throwaway repos.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

// gitCloneWithOrigin builds a bare origin repo with one commit on main and a
// working clone tracking it — the minimum needed for checkServiceFreshness
// to report a clean "up to date" state.
func gitCloneWithOrigin(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}

	origin := t.TempDir()
	runGit(t, origin, "init", "-q", "--bare")

	seed := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@example.invalid"},
		{"config", "user.name", "server test"},
		{"config", "commit.gpgsign", "false"},
	} {
		runGit(t, seed, args...)
	}
	if err := os.WriteFile(filepath.Join(seed, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, seed, "add", "main.go")
	runGit(t, seed, "commit", "-qm", "first")
	runGit(t, seed, "branch", "-M", "main")
	runGit(t, seed, "remote", "add", "origin", origin)
	runGit(t, seed, "push", "-q", "origin", "main")
	runGit(t, origin, "symbolic-ref", "HEAD", "refs/heads/main")

	clone := filepath.Join(t.TempDir(), "clone")
	cloneCmd := exec.Command("git", "clone", "-q", origin, clone)
	if out, err := cloneCmd.CombinedOutput(); err != nil {
		t.Fatalf("git clone: %v: %s", err, out)
	}
	return clone
}

// newFreshnessTestEnv mirrors newTestEnv but wires a git-backed service Dir
// and a freshness: config — newTestEnv's fixed config has neither, since
// its own tests don't need them.
func newFreshnessTestEnv(t *testing.T, freshness *config.FreshnessConfig) *testEnv {
	t.Helper()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("hello from svc"))
	}))
	t.Cleanup(upstream.Close)

	clone := gitCloneWithOrigin(t)
	proxyPort := freePort(t)

	cfg := &config.Config{
		Dir: t.TempDir(),
		Services: map[string]config.Service{
			"svc": {Dir: clone, Run: "sleep 30", Proxy: proxyPort, Entry: true},
		},
		Freshness: freshness,
	}

	rec := proxy.NewRecorder(proxy.RecorderOpts{Ring: 256})
	px := proxy.New(rec)
	lat := proxy.NewLatencyStore(func() float64 { return 0 })
	px.Latency = lat

	orch := orchestrator.New(cfg, px, orchestrator.Opts{LogDir: t.TempDir()})
	if err := orch.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	t.Cleanup(func() { orch.Down() })

	sessions := proxy.NewSessionManager(px, rec, []string{"svc"})
	t.Cleanup(sessions.Close)

	handler := server.New(server.Deps{
		Cfg: cfg, Orch: orch, Rec: rec, Lat: lat, Sessions: sessions, Version: "test",
	})
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	return &testEnv{ts: ts, upstream: upstream, rec: rec, lat: lat, sessions: sessions, orch: orch, cfg: cfg, proxyPort: proxyPort}
}

func TestFreshnessOmittedWhenNotConfigured(t *testing.T) {
	e := newTestEnv(t)
	_, body := e.get(t, "/api/status")

	var got struct {
		Services []map[string]json.RawMessage `json:"services"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Services) != 1 {
		t.Fatalf("services = %+v", got.Services)
	}
	if _, ok := got.Services[0]["freshness"]; ok {
		t.Error(`"freshness" key present in status despite no freshness: config`)
	}
}

func TestFreshnessCheckEndpointPopulatesStatus(t *testing.T) {
	e := newFreshnessTestEnv(t, &config.FreshnessConfig{DefaultBranch: "main", PollIntervalS: 3600})

	resp, body := e.post(t, "/api/freshness/check", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/freshness/check: status = %d, body = %s", resp.StatusCode, body)
	}

	_, statusBody := e.get(t, "/api/status")
	var status struct {
		Services []orchestrator.ServiceState `json:"services"`
	}
	if err := json.Unmarshal(statusBody, &status); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(status.Services) != 1 || status.Services[0].Freshness == nil {
		t.Fatalf("services = %+v, want svc with a populated Freshness", status.Services)
	}
	fr := status.Services[0].Freshness
	if fr.Branch != "main" || fr.Error != "" || fr.CheckedAt == "" {
		t.Errorf("Freshness = %+v, want a clean up-to-date check", fr)
	}
}
