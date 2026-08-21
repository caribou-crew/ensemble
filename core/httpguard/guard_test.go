package httpguard_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/caribou-crew/ensemble/core/httpguard"
)

// The guard is what stands in for authentication on an unauthenticated
// local control plane, so these cover both halves: DNS rebinding (a Host
// header naming the attacker's domain) and classic CSRF (a cross-origin
// browser POST that would otherwise restart services or run seeds).
//
// These six cases moved here verbatim from ensemble/server/guard_test.go
// when the guard was extracted (Task 4 Step 1) — same assertions, now
// exercising httpguard.Handler directly instead of through server.New.
// ensemble/server keeps one integration test proving the wiring survived.

// okHandler is the protected resource: reaching it at all means the guard
// let the request through.
func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func TestGuardRejectsForeignHost(t *testing.T) {
	ts := httptest.NewServer(httpguard.Handler(nil, okHandler()))
	defer ts.Close()

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/api/traffic", nil)
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
	ts := httptest.NewServer(httpguard.Handler(nil, okHandler()))
	defer ts.Close()

	for _, origin := range []string{"", "http://localhost:5173", "http://127.0.0.1:4700"} {
		req, err := http.NewRequest(http.MethodGet, ts.URL+"/api/health", nil)
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
	ts := httptest.NewServer(httpguard.Handler(nil, okHandler()))
	defer ts.Close()

	// A page on evil.example doing fetch(..., {mode:"no-cors"}): the
	// browser sends Origin, the response is unreadable to the attacker —
	// but without this check the restart still happens.
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/services/svc/restart", nil)
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
	ts := httptest.NewServer(httpguard.Handler(nil, okHandler()))
	defer ts.Close()

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/latency/reset", nil)
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
	ts := httptest.NewServer(httpguard.Handler(nil, okHandler()))
	defer ts.Close()

	// A cross-site form POST carries no readable Origin in every browser,
	// but Sec-Fetch-Site is set by the browser itself and can't be forged
	// by page script.
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/shutdown", nil)
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
	ts := httptest.NewServer(httpguard.Handler([]string{"dev-box.local"}, okHandler()))
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

// --- zero-value pins (global-constraints.md: a Go zero value must never
// mean "fine", and the rule must be pinned by a test that FAILS when the
// zero value is treated as permissive) ---

// A nil allowedHosts is the ZERO value of this parameter and it means
// "answer only as loopback" — NOT "no allow-list configured, so allow
// anything". Mutating Handler to treat nil as the "*" wildcard (i.e.
// `if len(allowedHosts) == 0 { allowedHosts = []string{"*"} }`) makes this
// test fail; without it that mutation is invisible, and every loopback
// listener in retrace passes nil.
func TestNilAllowedHostsIsLoopbackOnlyNotWideOpen(t *testing.T) {
	h := httpguard.Handler(nil, okHandler())

	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/api/health", nil)
	req.Host = "attacker.example"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("nil allowedHosts accepted Host %q: status = %d, want 403", req.Host, rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/shutdown", nil)
	req.Header.Set("Origin", "https://evil.example")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("nil allowedHosts accepted cross-origin: status = %d, want 403", rec.Code)
	}
}

// The "*" wildcard turns OFF host/origin matching, but it is a statement
// about the network, not an invitation to every website the developer has
// open: Sec-Fetch-Site is still enforced. Deleting that check from the
// wildcard path makes this fail.
func TestWildcardStillRejectsCrossSiteFetchMetadata(t *testing.T) {
	h := httpguard.Handler([]string{"*"}, okHandler())

	req := httptest.NewRequest(http.MethodPost, "http://anything.example/api/shutdown", nil)
	req.Host = "anything.example"
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("wildcard cross-site: status = %d, want 403", rec.Code)
	}
}

// --- the configured-hosts path (Task 13 is the first caller that passes a
// non-nil allowedHosts) ---

// A configured entry that is not the literal wildcard but reduces to one
// after the port/bracket stripping every OTHER entry gets — "*:8080",
// "[*]" — must not turn host/origin matching off. The stripping exists so
// "dev-box.local:4700" and "dev-box.local" mean the same thing; applying it
// before deciding whether the entry IS the wildcard let a single mistyped
// allow-list entry silently open the listener to any Host and any Origin,
// which is the whole DNS-rebinding surface this package exists to close.
//
// Deleting the exact-match check in newHostSet turns every case below into
// a 200.
func TestAnEntryThatOnlyLOOKSLikeTheWildcardDoesNotDisableMatching(t *testing.T) {
	for _, entry := range []string{"*:8080", "[*]", " *:80 ", "*.internal", "*.dev.example:443"} {
		t.Run(entry, func(t *testing.T) {
			h := httpguard.Handler([]string{entry}, okHandler())

			req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/api/health", nil)
			req.Host = "attacker.example"
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("allow-list %q accepted Host %q: status = %d, want 403", entry, req.Host, rec.Code)
			}

			// The Host header is only half of it: an entry read as the
			// wildcard also stops matching Origin, which is what makes a
			// cross-origin read of captured traffic possible.
			req = httptest.NewRequest(http.MethodGet, "http://127.0.0.1/api/queue", nil)
			req.Host = "127.0.0.1:4800"
			req.Header.Set("Origin", "https://evil.example")
			rec = httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("allow-list %q accepted Origin %q: status = %d, want 403", entry, "https://evil.example", rec.Code)
			}

			// The over-refusal mirror: failing closed on an entry we cannot
			// honour must not also break the loopback bind that entry was
			// added alongside.
			req = httptest.NewRequest(http.MethodGet, "http://127.0.0.1/api/health", nil)
			req.Host = "127.0.0.1:4800"
			rec = httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("allow-list %q rejected a loopback Host: status = %d, want 200", entry, rec.Code)
			}
		})
	}
}

// The mirror for the fix above, and the one most likely to be broken by an
// over-eager version of it: the literal "*" STILL turns host/origin
// matching off. Without this, "reject anything containing a star" passes
// every assertion in the test above and quietly removes the wildcard.
func TestTheLiteralWildcardStillDisablesHostAndOriginMatching(t *testing.T) {
	for _, hosts := range [][]string{{"*"}, {" * "}, {"build.internal", "*"}} {
		h := httpguard.Handler(hosts, okHandler())

		req := httptest.NewRequest(http.MethodGet, "http://anything.example/api/queue", nil)
		req.Host = "anything.example"
		req.Header.Set("Origin", "https://anything.example")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("allow-list %q rejected a foreign Host: status = %d, want 200 — the wildcard is what an unenumerable bind uses", hosts, rec.Code)
		}
	}
}

// A configured host is matched on the hostname alone, port ignored — the
// port a request arrives on is decided by the listener, not the allow-list.
// This is the mirror that keeps the fix above from being implemented as
// "reject any entry with a colon".
func TestAConfiguredHostWithAPortStillMatches(t *testing.T) {
	h := httpguard.Handler([]string{"build.internal:4800"}, okHandler())

	req := httptest.NewRequest(http.MethodGet, "http://build.internal/api/health", nil)
	req.Host = "build.internal:4800"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("configured host with a port: status = %d, want 200", rec.Code)
	}
}
