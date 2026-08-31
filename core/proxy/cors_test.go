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

	// The preflight itself must be recorded, tagged, and carry both what
	// the browser asked for and what ensemble actually allowed — so a
	// mismatch (e.g. a method the browser wants but AllowMethods omits)
	// is visible without ever reaching an upstream.
	hops := rec.Snapshot()
	if len(hops) != 1 {
		t.Fatalf("expected 1 recorded hop after preflight, got %d", len(hops))
	}
	ph := hops[0]
	if !ph.Preflight {
		t.Error("preflight hop must have Preflight = true")
	}
	if ph.Method != http.MethodOptions || ph.To != "public" || ph.Status != http.StatusNoContent {
		t.Errorf("preflight hop = %+v", ph)
	}
	if got := ph.Req.Headers["access-control-request-method"]; got != "PUT" {
		t.Errorf("preflight hop Req.Headers[access-control-request-method] = %q", got)
	}
	if got := ph.Resp.Headers["access-control-allow-methods"]; got != "GET, PUT" {
		t.Errorf("preflight hop Resp.Headers[access-control-allow-methods] = %q", got)
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

// A mixed-backend gateway: one route's backend (e.g. acme-svc) already
// emits its own CORS headers and must be left alone; another route's
// backend (e.g. widget) has none and still needs the gateway's cors:
// block. CORSPassthrough on the first route's Route is how that's told
// apart.
func TestGatewayCORSPassthroughRoute(t *testing.T) {
	rec := NewRecorder(RecorderOpts{Ring: 64})
	p := New(rec)
	defer p.Close()

	var upstreamCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		if r.URL.Path == "/passthrough/widgets" {
			w.Header().Set("Access-Control-Allow-Origin", "http://custom.example")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		fmt.Fprint(w, "ok")
	}))
	defer upstream.Close()

	gw, err := p.Serve(Target{
		Name:   "public",
		Listen: "127.0.0.1:0",
		Routes: []Route{
			{Prefix: "/passthrough", Upstream: upstream.URL, CORSPassthrough: true},
			{Prefix: "/", Upstream: upstream.URL},
		},
		CORS: &CORSPolicy{
			AllowOrigins: []string{"http://localhost:3000"},
			AllowMethods: []string{"GET", "PUT"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Passthrough route: preflight is forwarded upstream, not short-circuited
	// by the gateway, and the gateway adds no headers of its own.
	req, _ := http.NewRequest(http.MethodOptions, "http://"+gw+"/passthrough/widgets", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	req.Header.Set("Access-Control-Request-Method", "PUT")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if upstreamCalls != 1 {
		t.Errorf("passthrough preflight must reach upstream, got %d calls", upstreamCalls)
	}
	if got := resp.Header.Values("Access-Control-Allow-Origin"); len(got) != 1 || got[0] != "http://custom.example" {
		t.Errorf("passthrough preflight Access-Control-Allow-Origin = %v, want exactly [http://custom.example]", got)
	}

	// Passthrough route: a normal request also carries only the upstream's
	// own header — never doubled with the gateway's.
	req, _ = http.NewRequest(http.MethodGet, "http://"+gw+"/passthrough/widgets", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if got := resp.Header.Values("Access-Control-Allow-Origin"); len(got) != 1 || got[0] != "http://custom.example" {
		t.Errorf("passthrough GET Access-Control-Allow-Origin = %v, want exactly [http://custom.example]", got)
	}

	// A different route on the same gateway, without CORSPassthrough, is
	// unaffected: the gateway still short-circuits its preflight and adds
	// its own headers.
	callsBefore := upstreamCalls
	req, _ = http.NewRequest(http.MethodOptions, "http://"+gw+"/other", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	req.Header.Set("Access-Control-Request-Method", "PUT")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("non-passthrough preflight status = %d, want 204", resp.StatusCode)
	}
	if upstreamCalls != callsBefore {
		t.Errorf("non-passthrough preflight must not reach upstream, got %d extra calls", upstreamCalls-callsBefore)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "http://localhost:3000" {
		t.Errorf("non-passthrough Access-Control-Allow-Origin = %q, want gateway's own", got)
	}

	hops := rec.Snapshot()
	if len(hops) != 3 {
		t.Fatalf("expected 3 recorded hops, got %d", len(hops))
	}
	if hops[0].Preflight {
		t.Error("passthrough route's OPTIONS must record as a normal hop, not a synthetic preflight")
	}
	if !hops[2].Preflight {
		t.Error("non-passthrough route's OPTIONS must still record as a synthetic preflight hop")
	}
}
