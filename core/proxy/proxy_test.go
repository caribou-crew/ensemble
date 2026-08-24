package proxy

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/caribou-crew/ensemble/core/trace"
)

// forwardCtx copies trace headers the way a well-behaved service does —
// the sample stack's ~5-line propagation contract.
func forwardCtx(dst *http.Request, src *http.Request) {
	for _, k := range []string{"traceparent", "baggage"} {
		if v := src.Header.Get(k); v != "" {
			dst.Header.Set(k, v)
		}
	}
}

// TestThreeHopChainThroughOneProcess is the Task 1.5 integration test:
// client -> [proxy svc-a] -> svc-a -> [proxy svc-b] -> svc-b -> [proxy svc-c] -> svc-c,
// all three intercept listeners in this one process.
func TestThreeHopChainThroughOneProcess(t *testing.T) {
	rec := NewRecorder(RecorderOpts{Ring: 64, Redactor: trace.NewRedactor(nil, 65536)})
	p := New(rec)
	defer p.Close()

	// Terminal service.
	svcC := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		fmt.Fprint(w, `{"leaf":true}`)
	}))
	defer svcC.Close()
	proxyC, err := p.Serve(Target{Name: "svc-c", Listen: "127.0.0.1:0", Upstream: svcC.URL})
	if err != nil {
		t.Fatal(err)
	}

	// Middle service calls svc-c through its intercept port.
	svcB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req, _ := http.NewRequest("GET", "http://"+proxyC+"/leaf", nil)
		forwardCtx(req, r)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)
		fmt.Fprint(w, `{"middle":true}`)
	}))
	defer svcB.Close()
	proxyB, err := p.Serve(Target{Name: "svc-b", Listen: "127.0.0.1:0", Upstream: svcB.URL})
	if err != nil {
		t.Fatal(err)
	}

	// Front service calls svc-b through its intercept port.
	svcA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req, _ := http.NewRequest("GET", "http://"+proxyB+"/mid", nil)
		forwardCtx(req, r)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)
		fmt.Fprint(w, `{"front":true}`)
	}))
	defer svcA.Close()
	proxyA, err := p.Serve(Target{Name: "svc-a", Listen: "127.0.0.1:0", Upstream: svcA.URL})
	if err != nil {
		t.Fatal(err)
	}

	// The client call that sets the whole chain moving.
	req, _ := http.NewRequest("POST", "http://"+proxyA+"/front?q=1", strings.NewReader(`{"token":"secret-1"}`))
	req.Header.Set("authorization", "Bearer client-secret")
	req.Header.Set("content-type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(body) != `{"front":true}` {
		t.Fatalf("chain broken: %d %s", resp.StatusCode, body)
	}

	hops := rec.Snapshot()
	if len(hops) != 3 {
		t.Fatalf("want 3 hops, got %d: %+v", len(hops), hops)
	}
	byTo := map[string]trace.Hop{}
	for _, h := range hops {
		byTo[h.To] = h
	}
	a, b, c := byTo["svc-a"], byTo["svc-b"], byTo["svc-c"]

	// One trace, one correlation id, across all hops.
	if a.TraceID == "" || a.TraceID != b.TraceID || b.TraceID != c.TraceID {
		t.Fatalf("trace ids diverge: %q %q %q", a.TraceID, b.TraceID, c.TraceID)
	}
	if a.CorrelationID == "" || a.CorrelationID != b.CorrelationID || b.CorrelationID != c.CorrelationID {
		t.Fatalf("correlation ids diverge: %q %q %q", a.CorrelationID, b.CorrelationID, c.CorrelationID)
	}

	// Caller attribution via span ownership.
	if a.From != "" {
		t.Fatalf("client hop From should be empty, got %q", a.From)
	}
	if b.From != "svc-a" || c.From != "svc-b" {
		t.Fatalf("From chain wrong: b=%q c=%q", b.From, c.From)
	}

	// Span linkage: child hop's parent is the previous hop's span.
	if b.ParentSpanID != a.SpanID || c.ParentSpanID != b.SpanID {
		t.Fatalf("span linkage broken: a.span=%s b.parent=%s b.span=%s c.parent=%s",
			a.SpanID, b.ParentSpanID, b.SpanID, c.ParentSpanID)
	}

	// Capture quality.
	if a.Method != "POST" || a.Path != "/front?q=1" || a.Status != 200 {
		t.Fatalf("front hop wrong: %+v", a)
	}
	if a.Req.Headers["authorization"] != trace.Redacted {
		t.Fatalf("authorization not redacted: %q", a.Req.Headers["authorization"])
	}
	if a.Req.Body != `{"token":"secret-1"}` {
		t.Fatalf("request body not captured: %q", a.Req.Body)
	}
	if a.Resp.Body != `{"front":true}` {
		t.Fatalf("response body not captured: %q", a.Resp.Body)
	}
	if a.T.DoneMs <= 0 || a.T.FirstByteMs <= 0 || a.T.DoneMs < a.T.FirstByteMs {
		t.Fatalf("timings implausible: %+v", a.T)
	}
	if a.T.Start.IsZero() {
		t.Fatal("start time missing")
	}
}

func TestProxyPreservesClientTraceContext(t *testing.T) {
	rec := NewRecorder(RecorderOpts{Ring: 8})
	p := New(rec)
	defer p.Close()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The upstream must see a traceparent belonging to the same trace,
		// but a NEW span (the proxy's), so downstream linkage works.
		fmt.Fprint(w, r.Header.Get("traceparent"))
	}))
	defer upstream.Close()
	addr, err := p.Serve(Target{Name: "svc", Listen: "127.0.0.1:0", Upstream: upstream.URL})
	if err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequest("GET", "http://"+addr+"/x", nil)
	req.Header.Set("traceparent", "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	fwd, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if !strings.Contains(string(fwd), "0af7651916cd43dd8448eb211c80319c") {
		t.Fatalf("trace id not preserved: %s", fwd)
	}
	if strings.Contains(string(fwd), "b7ad6b7169203331") {
		t.Fatalf("span id not advanced: %s", fwd)
	}
	h := rec.Snapshot()[0]
	if h.TraceID != "0af7651916cd43dd8448eb211c80319c" || h.ParentSpanID != "b7ad6b7169203331" {
		t.Fatalf("hop context wrong: %+v", h)
	}
}

func TestProxyRecordsUpstreamFailureAsHop(t *testing.T) {
	rec := NewRecorder(RecorderOpts{Ring: 8})
	p := New(rec)
	defer p.Close()

	addr, err := p.Serve(Target{Name: "dead", Listen: "127.0.0.1:0", Upstream: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get("http://" + addr + "/x")
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("want 502, got %d", resp.StatusCode)
	}
	h := rec.Snapshot()[0]
	if h.Err == "" || h.Status != http.StatusBadGateway {
		t.Fatalf("failure hop wrong: %+v", h)
	}
}

func TestCapturedBodyIsValidJSONForRedaction(t *testing.T) {
	rec := NewRecorder(RecorderOpts{Ring: 8, Redactor: trace.NewRedactor([]string{"pan"}, 0)})
	p := New(rec)
	defer p.Close()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"pan":"4111111111111111","ok":true}`)
	}))
	defer upstream.Close()
	addr, err := p.Serve(Target{Name: "svc", Listen: "127.0.0.1:0", Upstream: upstream.URL})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get("http://" + addr + "/x")
	if err != nil {
		t.Fatal(err)
	}
	// The client must still receive the REAL body — redaction applies to
	// the recording, never the live traffic.
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil || parsed["pan"] != "4111111111111111" {
		t.Fatalf("live body altered: %s", body)
	}
	h := rec.Snapshot()[0]
	if strings.Contains(h.Resp.Body, "4111111111111111") {
		t.Fatalf("recorded body leaked pan: %s", h.Resp.Body)
	}
}

// TestCalledByFallbackAttribution: a call arrives with no traceparent at
// all — an un-instrumented backend whose HTTP client never propagates
// trace context, so SpanOwner has nothing to look up. Target.CalledBy
// gives the proxy a config-declared hint to fall back to instead of the
// synthetic "client" root, marked Attribution: "inferred" since it's a
// guess, not something derived from the trace itself.
func TestCalledByFallbackAttribution(t *testing.T) {
	rec := NewRecorder(RecorderOpts{Ring: 8})
	p := New(rec)
	defer p.Close()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer upstream.Close()
	addr, err := p.Serve(Target{Name: "backend", Listen: "127.0.0.1:0", Upstream: upstream.URL, CalledBy: []string{"bff"}})
	if err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get("http://" + addr + "/x")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	hops := rec.Snapshot()
	if len(hops) != 1 {
		t.Fatalf("want 1 hop, got %d", len(hops))
	}
	if hops[0].From != "bff" {
		t.Errorf("From = %q, want %q", hops[0].From, "bff")
	}
	if hops[0].Attribution != "inferred" {
		t.Errorf("Attribution = %q, want %q", hops[0].Attribution, "inferred")
	}
}

// TestCalledByFallbackAttributionAmbiguous: more than one CalledBy
// candidate can't be narrowed to a single caller, so all of them are
// surfaced jointly rather than silently picking one — still marked
// "inferred", never presented as if it were a real trace-derived fact.
func TestCalledByFallbackAttributionAmbiguous(t *testing.T) {
	rec := NewRecorder(RecorderOpts{Ring: 8})
	p := New(rec)
	defer p.Close()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer upstream.Close()
	addr, err := p.Serve(Target{Name: "backend", Listen: "127.0.0.1:0", Upstream: upstream.URL, CalledBy: []string{"bff", "acme-svc"}})
	if err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get("http://" + addr + "/x")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	hops := rec.Snapshot()
	if hops[0].From != "bff|acme-svc" {
		t.Errorf("From = %q, want %q", hops[0].From, "bff|acme-svc")
	}
	if hops[0].Attribution != "inferred" {
		t.Errorf("Attribution = %q, want %q", hops[0].Attribution, "inferred")
	}
}

// TestCalledByFallbackDoesNotOverrideRealAttribution: when the caller DOES
// propagate trace context, SpanOwner resolves the real caller and the
// CalledBy hint (deliberately wrong here) must be ignored entirely — real
// attribution always wins over a config guess.
func TestCalledByFallbackDoesNotOverrideRealAttribution(t *testing.T) {
	rec := NewRecorder(RecorderOpts{Ring: 8})
	p := New(rec)
	defer p.Close()

	backendUp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer backendUp.Close()
	backendAddr, err := p.Serve(Target{Name: "backend", Listen: "127.0.0.1:0", Upstream: backendUp.URL, CalledBy: []string{"wrong-guess"}})
	if err != nil {
		t.Fatal(err)
	}

	callerUp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req, _ := http.NewRequest("GET", "http://"+backendAddr+"/x", nil)
		forwardCtx(req, r)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)
		w.Write([]byte("ok"))
	}))
	defer callerUp.Close()
	callerAddr, err := p.Serve(Target{Name: "caller", Listen: "127.0.0.1:0", Upstream: callerUp.URL})
	if err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get("http://" + callerAddr + "/y")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	hops := rec.Snapshot()
	var backendHop *trace.Hop
	for i := range hops {
		if hops[i].To == "backend" {
			backendHop = &hops[i]
		}
	}
	if backendHop == nil {
		t.Fatalf("no hop recorded to backend: %+v", hops)
	}
	if backendHop.From != "caller" {
		t.Errorf("From = %q, want %q", backendHop.From, "caller")
	}
	if backendHop.Attribution != "" {
		t.Errorf("Attribution = %q, want empty (real attribution, not inferred)", backendHop.Attribution)
	}
}

// TestCallerHeaderDeclaresAttribution: a caller ensemble doesn't manage
// (never claims a span, has no CalledBy hint) can self-identify via the
// X-Ensemble-Caller request header — honored as From, marked "declared"
// rather than "inferred" since it's an assertion from the actual caller,
// not a static config guess.
func TestCallerHeaderDeclaresAttribution(t *testing.T) {
	rec := NewRecorder(RecorderOpts{Ring: 8})
	p := New(rec)
	defer p.Close()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer upstream.Close()
	addr, err := p.Serve(Target{Name: "backend", Listen: "127.0.0.1:0", Upstream: upstream.URL})
	if err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequest("GET", "http://"+addr+"/x", nil)
	req.Header.Set("X-Ensemble-Caller", "external-app")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	hops := rec.Snapshot()
	if len(hops) != 1 {
		t.Fatalf("want 1 hop, got %d", len(hops))
	}
	if hops[0].From != "external-app" {
		t.Errorf("From = %q, want %q", hops[0].From, "external-app")
	}
	if hops[0].Attribution != "declared" {
		t.Errorf("Attribution = %q, want %q", hops[0].Attribution, "declared")
	}
}

// TestCallerHeaderDoesNotOverrideRealAttribution: a caller ensemble DOES
// manage still wins via real trace-context propagation even if it (oddly)
// also sent the header — a self-declared name is never allowed to shadow
// ground truth.
func TestCallerHeaderDoesNotOverrideRealAttribution(t *testing.T) {
	rec := NewRecorder(RecorderOpts{Ring: 8})
	p := New(rec)
	defer p.Close()

	backendUp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer backendUp.Close()
	backendAddr, err := p.Serve(Target{Name: "backend", Listen: "127.0.0.1:0", Upstream: backendUp.URL})
	if err != nil {
		t.Fatal(err)
	}

	callerUp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req, _ := http.NewRequest("GET", "http://"+backendAddr+"/x", nil)
		forwardCtx(req, r)
		req.Header.Set("X-Ensemble-Caller", "wrong-self-declared-name")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)
		w.Write([]byte("ok"))
	}))
	defer callerUp.Close()
	callerAddr, err := p.Serve(Target{Name: "caller", Listen: "127.0.0.1:0", Upstream: callerUp.URL})
	if err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get("http://" + callerAddr + "/y")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	hops := rec.Snapshot()
	var backendHop *trace.Hop
	for i := range hops {
		if hops[i].To == "backend" {
			backendHop = &hops[i]
		}
	}
	if backendHop == nil {
		t.Fatalf("no hop recorded to backend: %+v", hops)
	}
	if backendHop.From != "caller" {
		t.Errorf("From = %q, want %q", backendHop.From, "caller")
	}
	if backendHop.Attribution != "" {
		t.Errorf("Attribution = %q, want empty (real attribution, not declared)", backendHop.Attribution)
	}
}

// TestCallerHeaderTakesPrecedenceOverCalledBy: the header is a live
// assertion from the actual request, so it wins over the static CalledBy
// config guess when both are present.
func TestCallerHeaderTakesPrecedenceOverCalledBy(t *testing.T) {
	rec := NewRecorder(RecorderOpts{Ring: 8})
	p := New(rec)
	defer p.Close()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer upstream.Close()
	addr, err := p.Serve(Target{Name: "backend", Listen: "127.0.0.1:0", Upstream: upstream.URL, CalledBy: []string{"bff"}})
	if err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequest("GET", "http://"+addr+"/x", nil)
	req.Header.Set("X-Ensemble-Caller", "external-app")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	hops := rec.Snapshot()
	if hops[0].From != "external-app" {
		t.Errorf("From = %q, want %q", hops[0].From, "external-app")
	}
	if hops[0].Attribution != "declared" {
		t.Errorf("Attribution = %q, want %q", hops[0].Attribution, "declared")
	}
}

// TestProxyTraceHeaderAdoptsCustomTraceID: Proxy.TraceHeader names a
// stack's own correlation header (e.g. "x-local-trace-id"). With no
// traceparent on the inbound request, its value is adopted as the hop's
// TraceID instead of minting an unrelated one.
func TestProxyTraceHeaderAdoptsCustomTraceID(t *testing.T) {
	rec := NewRecorder(RecorderOpts{Ring: 8})
	p := New(rec)
	p.TraceHeader = "x-local-trace-id"
	defer p.Close()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer upstream.Close()
	addr, err := p.Serve(Target{Name: "backend", Listen: "127.0.0.1:0", Upstream: upstream.URL})
	if err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequest("GET", "http://"+addr+"/x", nil)
	req.Header.Set("x-local-trace-id", "company-corr-abc123")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	hops := rec.Snapshot()
	if len(hops) != 1 {
		t.Fatalf("want 1 hop, got %d", len(hops))
	}
	if hops[0].TraceID != "company-corr-abc123" {
		t.Errorf("TraceID = %q, want the custom header's value", hops[0].TraceID)
	}
}

// TestProxyTraceHeaderStitchesChainAcrossUninstrumentedHop is the actual
// bug this exists to fix: an un-instrumented middle service (svcA) that
// doesn't forward traceparent but DOES forward the company's own
// correlation header (its own established convention, unrelated to W3C
// tracing) must still land both hops in the SAME trace, so the dashboard
// groups and causally orders them together instead of splitting them into
// two unrelated traces.
func TestProxyTraceHeaderStitchesChainAcrossUninstrumentedHop(t *testing.T) {
	rec := NewRecorder(RecorderOpts{Ring: 8})
	p := New(rec)
	p.TraceHeader = "x-local-trace-id"
	defer p.Close()

	svcBUp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer svcBUp.Close()
	proxyB, err := p.Serve(Target{Name: "svc-b", Listen: "127.0.0.1:0", Upstream: svcBUp.URL})
	if err != nil {
		t.Fatal(err)
	}

	// svcA is the un-instrumented middle hop: it forwards the company's
	// own correlation header (as its real process would, by their
	// existing convention) but NOT traceparent (it has no idea what W3C
	// tracing is) — exactly the scenario that used to mint svcA -> svcB a
	// wholly unrelated trace id.
	svcAUp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req, _ := http.NewRequest("GET", "http://"+proxyB+"/inner", nil)
		req.Header.Set("x-local-trace-id", r.Header.Get("x-local-trace-id"))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)
		w.Write([]byte("ok"))
	}))
	defer svcAUp.Close()
	proxyA, err := p.Serve(Target{Name: "svc-a", Listen: "127.0.0.1:0", Upstream: svcAUp.URL})
	if err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequest("GET", "http://"+proxyA+"/outer", nil)
	req.Header.Set("x-local-trace-id", "company-corr-xyz789")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	hops := rec.Snapshot()
	if len(hops) != 2 {
		t.Fatalf("want 2 hops, got %d: %+v", len(hops), hops)
	}
	if hops[0].TraceID != hops[1].TraceID {
		t.Errorf("hops landed in different traces: %q vs %q", hops[0].TraceID, hops[1].TraceID)
	}
	if hops[0].TraceID != "company-corr-xyz789" {
		t.Errorf("TraceID = %q, want the company's own correlation id", hops[0].TraceID)
	}
}
