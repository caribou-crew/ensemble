package server_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/caribou-crew/ensemble/ensemble/server"
)

// The guard is what stands in for authentication on an unauthenticated
// local control plane, so these cover both halves: DNS rebinding (a Host
// header naming the attacker's domain) and classic CSRF (a cross-origin
// browser POST that would otherwise restart services or run seeds).

func TestGuardRejectsForeignHost(t *testing.T) {
	env := newTestEnv(t)

	req, err := http.NewRequest(http.MethodGet, env.ts.URL+"/api/traffic", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Host = "attacker.example"

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("rebound Host: status = %d, want 403", resp.StatusCode)
	}
}

func TestGuardAllowsLoopbackHostAndOrigin(t *testing.T) {
	env := newTestEnv(t)

	for _, origin := range []string{"", "http://localhost:5173", "http://127.0.0.1:4700"} {
		req, err := http.NewRequest(http.MethodGet, env.ts.URL+"/api/health", nil)
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("do: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("origin %q: status = %d, want 200", origin, resp.StatusCode)
		}
	}
}

func TestGuardRejectsCrossOriginMutation(t *testing.T) {
	env := newTestEnv(t)

	// A page on evil.example doing fetch(..., {mode:"no-cors"}): the
	// browser sends Origin, the response is unreadable to the attacker —
	// but without this check the restart still happens.
	req, err := http.NewRequest(http.MethodPost, env.ts.URL+"/api/services/svc/restart", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Origin", "https://evil.example")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin restart: status = %d, want 403", resp.StatusCode)
	}
}

func TestGuardRejectsNullOrigin(t *testing.T) {
	env := newTestEnv(t)

	req, err := http.NewRequest(http.MethodPost, env.ts.URL+"/api/latency/reset", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Origin", "null") // sandboxed iframe / file:// page

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("null origin: status = %d, want 403", resp.StatusCode)
	}
}

func TestGuardRejectsCrossSiteFetchMetadata(t *testing.T) {
	env := newTestEnv(t)

	// A cross-site form POST carries no readable Origin in every browser,
	// but Sec-Fetch-Site is set by the browser itself and can't be forged
	// by page script.
	req, err := http.NewRequest(http.MethodPost, env.ts.URL+"/api/shutdown", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Sec-Fetch-Site", "cross-site")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-site shutdown: status = %d, want 403", resp.StatusCode)
	}
}

func TestGuardAllowsConfiguredNonLoopbackHost(t *testing.T) {
	// Only /api/health is exercised, so the bare Deps below is enough —
	// no orchestrator/proxy wiring needed to prove the host allow-list.
	ts := httptest.NewServer(server.New(server.Deps{
		Version:      "test",
		AllowedHosts: []string{"dev-box.local"},
	}))
	defer ts.Close()

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/api/health", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Host = "dev-box.local:4700"

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("configured host: status = %d, want 200", resp.StatusCode)
	}
}
