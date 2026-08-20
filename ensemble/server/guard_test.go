package server_test

import (
	"net/http"
	"testing"
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
