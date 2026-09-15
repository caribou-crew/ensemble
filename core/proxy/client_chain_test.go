package proxy

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/caribou-crew/ensemble/core/trace"
)

// forwardTraceOnly is a service that propagates W3C trace context but NOT
// baggage — a real and common shape, since tracing libraries instrument
// traceparent far more often than they carry baggage.
func forwardTraceOnly(dst *http.Request, src *http.Request) {
	if v := src.Header.Get("traceparent"); v != "" {
		dst.Header.Set("traceparent", v)
	}
}

// twoHopChain runs client -> [proxy svc-a] -> svc-a -> [proxy svc-b] -> svc-b
// with the client sending clientHeader, and returns the recorded hops
// oldest-first. forward controls what svc-a propagates.
func twoHopChain(t *testing.T, clientHeader string, forward func(dst, src *http.Request)) []trace.Hop {
	t.Helper()
	rec := NewRecorder(RecorderOpts{Ring: 64, Redactor: mustRedactor(t, nil, 65536)})
	p := New(rec)
	defer p.Close()

	svcB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"leaf":true}`)
	}))
	defer svcB.Close()
	proxyB, err := p.Serve(Target{Name: "svc-b", Listen: "127.0.0.1:0", Upstream: svcB.URL})
	if err != nil {
		t.Fatal(err)
	}

	svcA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req, _ := http.NewRequest("GET", "http://"+proxyB+"/leaf", nil)
		forward(req, r)
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

	req, _ := http.NewRequest("GET", "http://"+proxyA+"/home", nil)
	if clientHeader != "" {
		req.Header.Set("x-source-client", clientHeader)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	hops := rec.Snapshot()
	if len(hops) != 2 {
		t.Fatalf("recorded %d hops, want 2", len(hops))
	}
	return hops
}

// The point of carrying client identity in baggage: the downstream hop —
// which never saw the client's header — reports the front-end that started
// the chain, on the hop itself, with no inference needed.
func TestClientIdentityPropagatesThroughBaggage(t *testing.T) {
	for _, h := range twoHopChain(t, "app-legacy", forwardCtx) {
		if h.Client != "app-legacy" {
			t.Errorf("hop to %q recorded client %q, want %q — the identity must reach every hop of the chain, not just the edge",
				h.To, h.Client, "app-legacy")
		}
	}
}

// A service that keeps traceparent but drops baggage leaves its downstream
// hop with no client of its own. That hop must NOT invent one — and
// trace.ResolveClient is what recovers it at read time.
func TestClientIdentityStopsAtAServiceThatDropsBaggage(t *testing.T) {
	hops := twoHopChain(t, "app-legacy", forwardTraceOnly)

	var edge, inner trace.Hop
	for _, h := range hops {
		switch h.To {
		case "svc-a":
			edge = h
		case "svc-b":
			inner = h
		}
	}
	if edge.Client != "app-legacy" {
		t.Fatalf("edge hop client = %q, want %q", edge.Client, "app-legacy")
	}
	if inner.Client != "" {
		t.Fatalf("inner hop client = %q, want \"\" — nothing propagated, and the hop must say so rather than guess", inner.Client)
	}
	if inner.TraceID == "" || inner.TraceID != edge.TraceID {
		t.Fatalf("inner traceId = %q, edge traceId = %q — the read-time fallback needs them to match", inner.TraceID, edge.TraceID)
	}
	got, ok := trace.ResolveClient(inner, trace.ClientIndex(hops))
	if !ok || got != "app-legacy" {
		t.Fatalf("ResolveClient(inner) = (%q, %v), want (%q, true)", got, ok, "app-legacy")
	}
}

// An inner service that declares a DIFFERENT client must not relabel the
// chain: the field answers "which front-end started this", and a downstream
// re-declaration is a bug or a forgery, not a new origin.
func TestPropagatedClientIdentityWinsOverADownstreamHeader(t *testing.T) {
	relabel := func(dst, src *http.Request) {
		forwardCtx(dst, src)
		dst.Header.Set("x-source-client", "app-next")
	}
	for _, h := range twoHopChain(t, "app-legacy", relabel) {
		if h.Client != "app-legacy" {
			t.Errorf("hop to %q recorded client %q, want %q — an inherited identity outranks a header on a later hop",
				h.To, h.Client, "app-legacy")
		}
	}
}

// Malformed baggage is not a misconfigured app to be reported with the
// fallback bucket; it is corruption or injection, and parking it under a
// real-looking identity is the failure to avoid.
func TestMalformedPropagatedClientIsDroppedNotBucketed(t *testing.T) {
	if got := validClientIdentity("Not A Client"); got != "" {
		t.Errorf("validClientIdentity(malformed) = %q, want \"\" — never FallbackClient", got)
	}
	if got := validClientIdentity("app-next"); got != "app-next" {
		t.Errorf("validClientIdentity(valid) = %q, want %q", got, "app-next")
	}
}

// The unattributed sentinel must be unrepresentable as a client identity.
// This is the assertion that belongs next to ValidClient itself: loosen the
// charset to admit "(none)" and this fails.
func TestUnattributedSentinelIsNotAValidClientIdentity(t *testing.T) {
	if ValidClient.MatchString(trace.UnattributedClient) {
		t.Fatalf("ValidClient admits %q — a real client could collide with the unattributed filter sentinel",
			trace.UnattributedClient)
	}
}
