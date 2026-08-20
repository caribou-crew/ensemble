package proxy

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ensemble-dev/ensemble/core/trace"
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
