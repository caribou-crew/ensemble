package proxy

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCORSPolicyHeaders(t *testing.T) {
	wildcard := &CORSPolicy{AllowOrigins: []string{"*"}, AllowMethods: []string{"GET", "POST"}}
	if _, ok := wildcard.headers(""); ok {
		t.Error("empty origin: expected no headers")
	}
	h, ok := wildcard.headers("http://localhost:3000")
	if !ok || h.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("wildcard origin: got (%v, %v)", h, ok)
	}
	if h.Get("Vary") != "" {
		t.Errorf("wildcard origin should not set Vary, got %q", h.Get("Vary"))
	}

	allowlist := &CORSPolicy{AllowOrigins: []string{"http://a.example"}, AllowCredentials: true}
	if _, ok := allowlist.headers("http://b.example"); ok {
		t.Error("origin not in allowlist: expected no headers")
	}
	h, ok = allowlist.headers("http://a.example")
	if !ok || h.Get("Access-Control-Allow-Origin") != "http://a.example" || h.Get("Access-Control-Allow-Credentials") != "true" {
		t.Fatalf("allowlisted origin: got (%v, %v)", h, ok)
	}
	if h.Get("Vary") != "Origin" {
		t.Errorf("reflected origin should Vary: Origin, got %q", h.Get("Vary"))
	}

	var nilPolicy *CORSPolicy
	if _, ok := nilPolicy.headers("http://a.example"); ok {
		t.Error("nil policy: expected no headers")
	}
}

func TestIsPreflight(t *testing.T) {
	req := httptest.NewRequest(http.MethodOptions, "/x", nil)
	if isPreflight(req) {
		t.Error("OPTIONS without Access-Control-Request-Method must not be a preflight")
	}
	req.Header.Set("Access-Control-Request-Method", "PUT")
	if !isPreflight(req) {
		t.Error("OPTIONS with Access-Control-Request-Method must be a preflight")
	}
	req = httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Access-Control-Request-Method", "PUT")
	if isPreflight(req) {
		t.Error("a non-OPTIONS request must never be treated as a preflight")
	}
}

// TestGatewayCORSPreflightAndHeaders drives a real gateway listener with a
// CORS policy end to end: preflight is answered directly (no upstream
// call), and a normal cross-origin request forwards as usual with CORS
// headers merged onto whatever the upstream returns.
func TestGatewayCORSPreflightAndHeaders(t *testing.T) {
	rec := NewRecorder(RecorderOpts{Ring: 64})
	p := New(rec)
	defer p.Close()

	var upstreamCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		fmt.Fprint(w, "ok")
	}))
	defer upstream.Close()

	gw, err := p.Serve(Target{
		Name:   "public",
		Listen: "127.0.0.1:0",
		Routes: []Route{{Prefix: "/", Upstream: upstream.URL}},
		CORS: &CORSPolicy{
			AllowOrigins:  []string{"http://localhost:3000"},
			AllowMethods:  []string{"GET", "PUT"},
			MaxAgeSeconds: 600,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Preflight: answered directly, no upstream call.
	req, _ := http.NewRequest(http.MethodOptions, "http://"+gw+"/widgets/1", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	req.Header.Set("Access-Control-Request-Method", "PUT")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("preflight status = %d, want 204", resp.StatusCode)
	}
	if got := resp.Header.Get("Access-Control-Allow-Methods"); got != "GET, PUT" {
		t.Errorf("Access-Control-Allow-Methods = %q", got)
	}
	if got := resp.Header.Get("Access-Control-Max-Age"); got != "600" {
		t.Errorf("Access-Control-Max-Age = %q", got)
	}
	if upstreamCalls != 0 {
		t.Errorf("preflight must not call upstream, got %d calls", upstreamCalls)
	}

	// Normal cross-origin request: forwarded, CORS headers still present.
	req, _ = http.NewRequest(http.MethodGet, "http://"+gw+"/widgets/1", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "ok" || upstreamCalls != 1 {
		t.Errorf("expected forwarded request to hit upstream once, got body %q, calls %d", body, upstreamCalls)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "http://localhost:3000" {
		t.Errorf("Access-Control-Allow-Origin = %q", got)
	}

	// Disallowed origin: no CORS headers, request still forwarded normally.
	req, _ = http.NewRequest(http.MethodGet, "http://"+gw+"/widgets/1", nil)
	req.Header.Set("Origin", "http://evil.example")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("disallowed origin must not get Access-Control-Allow-Origin, got %q", got)
	}
	if upstreamCalls != 2 {
		t.Errorf("disallowed origin must still forward to upstream, got %d calls", upstreamCalls)
	}
}
