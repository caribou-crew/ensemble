package server_test

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/caribou-crew/ensemble/ensemble/server"
)

// The guard's own six cases moved to core/httpguard/guard_test.go when the
// guard was extracted (Task 4 Step 1) — that is where the DNS-rebinding,
// null-origin, cross-site and configured-host rules are pinned now.
//
// This one stays behind on purpose: it is the only test that proves the
// WIRING survived the move, i.e. that server.New still wraps its mux in the
// guard and passes Deps.AllowedHosts through to it. Without it, deleting
// the guard call in server.New would leave every remaining test green.
func TestServerRejectsCrossOriginRequests(t *testing.T) {
	env := newTestEnv(t)

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
		t.Fatalf("cross-origin restart through server.New: status = %d, want 403", resp.StatusCode)
	}
}

// TestServerPassesAllowedHostsToTheGuard is the wiring test
// TestServerRejectsCrossOriginRequests' comment claimed to be but was not:
// proof that Deps.AllowedHosts actually reaches the guard through
// server.New, not just that SOME guard call exists. Mutating server.go's
// `return guard(d.AllowedHosts, mux)` to `return guard(nil, mux)` leaves
// every other test in this package green — a configured non-loopback host
// (--allowed-hosts, or a wide "*" bind) would silently 403 every request in
// production with nothing here to catch it.
func TestServerPassesAllowedHostsToTheGuard(t *testing.T) {
	handler := server.New(server.Deps{Version: "test", AllowedHosts: []string{"dev-box.local"}})
	ts := httptest.NewServer(handler)
	defer ts.Close()

	addr := ts.Listener.Addr().String()
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split listener addr %q: %v", addr, err)
	}

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/api/health", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Host = "dev-box.local:" + port

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/health with Host: dev-box.local through server.New: status = %d, want 200 — "+
			"Deps.AllowedHosts did not reach the guard", resp.StatusCode)
	}
}
