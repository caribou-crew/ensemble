package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caribou-crew/ensemble/core/proxy"
	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/ensemble/config"
)

func doctorPort(t *testing.T, addr string) int {
	t.Helper()
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split proxy address %q: %v", addr, err)
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		t.Fatalf("parse proxy port %q: %v", port, err)
	}
	return n
}

func TestDoctorPassesRealMultiHopProxyChain(t *testing.T) {
	leaf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(leaf.Close)

	rec := proxy.NewRecorder(proxy.RecorderOpts{Ring: 64})
	px := proxy.New(rec)
	t.Cleanup(px.Close)

	leafAddr, err := px.Serve(proxy.Target{Name: "leaf", Listen: "127.0.0.1:0", Upstream: leaf.URL})
	if err != nil {
		t.Fatalf("serve leaf proxy: %v", err)
	}
	entryUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req, reqErr := http.NewRequestWithContext(r.Context(), http.MethodGet, "http://"+leafAddr+"/leaf", nil)
		if reqErr != nil {
			http.Error(w, reqErr.Error(), http.StatusInternalServerError)
			return
		}
		req.Header.Set("traceparent", r.Header.Get("traceparent"))
		req.Header.Set("baggage", r.Header.Get("baggage"))
		resp, doErr := http.DefaultClient.Do(req)
		if doErr != nil {
			http.Error(w, doErr.Error(), http.StatusBadGateway)
			return
		}
		resp.Body.Close()
		w.WriteHeader(resp.StatusCode)
	}))
	t.Cleanup(entryUpstream.Close)
	entryAddr, err := px.Serve(proxy.Target{Name: "entry", Listen: "127.0.0.1:0", Upstream: entryUpstream.URL})
	if err != nil {
		t.Fatalf("serve entry proxy: %v", err)
	}

	sessions := proxy.NewSessionManager(px, rec, []string{"entry"})
	t.Cleanup(sessions.Close)
	s := &server{Deps: Deps{
		Cfg: &config.Config{Services: map[string]config.Service{
			"entry": {Proxy: doctorPort(t, entryAddr)},
			"leaf":  {Proxy: doctorPort(t, leafAddr)},
		}},
		Rec: rec, Sessions: sessions,
	}}

	body, err := json.Marshal(DoctorRequest{Target: "entry", Path: "/probe", Expect: []string{"leaf"}, TimeoutMs: 1000})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/doctor", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	s.handleDoctor(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var got DoctorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v\n%s", err, rr.Body.String())
	}
	if got.Verdict != DoctorPass {
		t.Fatalf("verdict = %q, want pass; reasons=%v hops=%+v", got.Verdict, got.Reasons, got.Hops)
	}
	if got.HTTPStatus != http.StatusNoContent || got.TraceID == "" || got.SessionID == "" {
		t.Fatalf("probe identity/status missing: %+v", got)
	}
	if len(got.MissingExpected) != 0 {
		t.Fatalf("missing expected = %v", got.MissingExpected)
	}
	if !got.Propagation.TraceContext || !got.Propagation.SessionContext || !got.Propagation.Linked {
		t.Fatalf("propagation = %+v", got.Propagation)
	}
	seen := map[string]bool{}
	for _, h := range got.Hops {
		seen[h.To] = true
		if h.TraceID != got.TraceID {
			t.Errorf("hop escaped probe identity: %+v", h)
		}
	}
	if !seen["entry"] || !seen["leaf"] {
		t.Fatalf("real configured hops not observed: %+v", got.Hops)
	}
	if sessions.Get(got.SessionID) != nil {
		t.Fatal("doctor left its temporary session active")
	}
}

func newDoctorSingleTarget(t *testing.T, upstream http.Handler) (*server, *proxy.SessionManager) {
	t.Helper()
	app := httptest.NewServer(upstream)
	t.Cleanup(app.Close)
	rec := proxy.NewRecorder(proxy.RecorderOpts{Ring: 64})
	px := proxy.New(rec)
	t.Cleanup(px.Close)
	addr, err := px.Serve(proxy.Target{Name: "entry", Listen: "127.0.0.1:0", Upstream: app.URL})
	if err != nil {
		t.Fatalf("serve target proxy: %v", err)
	}
	sessions := proxy.NewSessionManager(px, rec, []string{"entry"})
	t.Cleanup(sessions.Close)
	return &server{Deps: Deps{
		Cfg:      &config.Config{Services: map[string]config.Service{"entry": {Proxy: doctorPort(t, addr)}}},
		Rec:      rec,
		Sessions: sessions,
	}}, sessions
}

func runDoctor(t *testing.T, s *server, in DoctorRequest) (int, DoctorResponse, string) {
	t.Helper()
	body, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/doctor", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	s.handleDoctor(rr, req)
	var got DoctorResponse
	if rr.Code == http.StatusOK {
		if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode response: %v\n%s", err, rr.Body.String())
		}
	}
	return rr.Code, got, rr.Body.String()
}

func TestDoctorAcceptsCompletedChunkedResponse(t *testing.T) {
	s, _ := newDoctorSingleTarget(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("test server has no flusher")
		}
		_, _ = io.WriteString(w, "first")
		flusher.Flush()
		_, _ = io.WriteString(w, "second")
	}))

	status, got, body := runDoctor(t, s, DoctorRequest{Target: "entry", Path: "/stream", TimeoutMs: 1000})
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %s", status, body)
	}
	if got.Verdict != DoctorPass {
		t.Fatalf("completed chunked response verdict = %q, reasons=%v hops=%+v", got.Verdict, got.Reasons, got.Hops)
	}
	if len(got.Hops) == 0 || !containsString(got.Hops[0].Quality, "timing-unavailable") && got.Hops[0].DurationMs <= 0 {
		t.Fatalf("completed response lacks final duration: %+v", got.Hops)
	}
}

type doctorForwardOptions struct {
	dropTrace, dropBaggage, bypassLeaf bool
	leafStatus, entryStatus            int
}

func newDoctorForwardingChain(t *testing.T, opts doctorForwardOptions) (*server, *proxy.SessionManager) {
	t.Helper()
	leaf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		status := opts.leafStatus
		if status == 0 {
			status = http.StatusNoContent
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(leaf.Close)
	rec := proxy.NewRecorder(proxy.RecorderOpts{Ring: 64})
	px := proxy.New(rec)
	t.Cleanup(px.Close)
	leafAddr, err := px.Serve(proxy.Target{Name: "leaf", Listen: "127.0.0.1:0", Upstream: leaf.URL})
	if err != nil {
		t.Fatalf("serve leaf proxy: %v", err)
	}
	entryUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target := "http://" + leafAddr + "/leaf"
		if opts.bypassLeaf {
			target = leaf.URL + "/leaf"
		}
		req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, target, nil)
		if !opts.dropTrace {
			req.Header.Set("traceparent", r.Header.Get("traceparent"))
		}
		if !opts.dropBaggage {
			req.Header.Set("baggage", r.Header.Get("baggage"))
		}
		resp, doErr := http.DefaultClient.Do(req)
		if doErr != nil {
			http.Error(w, doErr.Error(), http.StatusBadGateway)
			return
		}
		resp.Body.Close()
		status := resp.StatusCode
		if opts.entryStatus != 0 {
			status = opts.entryStatus
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(entryUpstream.Close)
	entryAddr, err := px.Serve(proxy.Target{Name: "entry", Listen: "127.0.0.1:0", Upstream: entryUpstream.URL})
	if err != nil {
		t.Fatalf("serve entry proxy: %v", err)
	}
	sessions := proxy.NewSessionManager(px, rec, []string{"entry"})
	t.Cleanup(sessions.Close)
	return &server{Deps: Deps{
		Cfg: &config.Config{Services: map[string]config.Service{
			"entry": {Proxy: doctorPort(t, entryAddr)},
			"leaf":  {Proxy: doctorPort(t, leafAddr)},
		}},
		Rec: rec, Sessions: sessions,
	}}, sessions
}

func TestDoctorFailsOnPropagationAndExpectedPathGaps(t *testing.T) {
	tests := []struct {
		name        string
		dropTrace   bool
		dropBaggage bool
		bypassLeaf  bool
		wantGap     bool
	}{
		{name: "baggage dropped", dropBaggage: true, wantGap: true},
		{name: "trace context dropped", dropTrace: true},
		{name: "expected proxy bypassed", bypassLeaf: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, sessions := newDoctorForwardingChain(t, doctorForwardOptions{dropTrace: tc.dropTrace, dropBaggage: tc.dropBaggage, bypassLeaf: tc.bypassLeaf})
			status, got, body := runDoctor(t, s, DoctorRequest{Target: "entry", Path: "/probe", Expect: []string{"leaf"}, TimeoutMs: 1000})
			if status != http.StatusOK {
				t.Fatalf("status = %d, body = %s", status, body)
			}
			if got.Verdict != DoctorFail {
				t.Fatalf("verdict = %q, want fail; reasons=%v propagation=%+v hops=%+v", got.Verdict, got.Reasons, got.Propagation, got.Hops)
			}
			if tc.dropTrace && got.Propagation.TraceContext {
				t.Fatal("dropped trace context reported intact")
			}
			if tc.dropBaggage && got.Propagation.SessionContext {
				t.Fatal("dropped session baggage reported intact")
			}
			if tc.bypassLeaf && !containsString(got.MissingExpected, "leaf") {
				t.Fatalf("bypassed leaf missing list = %v", got.MissingExpected)
			}
			if tc.wantGap && len(got.Propagation.Gaps) == 0 {
				t.Fatal("known propagation gap was not explained")
			}
			if sessions.Get(got.SessionID) != nil {
				t.Fatal("failed doctor left its temporary session active")
			}
		})
	}
}

func TestDoctorFailsWhenDownstreamHopFailsBehindSuccessfulEntry(t *testing.T) {
	s, _ := newDoctorForwardingChain(t, doctorForwardOptions{leafStatus: http.StatusInternalServerError, entryStatus: http.StatusNoContent})
	_, got, _ := runDoctor(t, s, DoctorRequest{Target: "entry", Path: "/probe", Expect: []string{"leaf"}, TimeoutMs: 1000})
	if got.HTTPStatus != http.StatusNoContent {
		t.Fatalf("entry status = %d, want successful 204", got.HTTPStatus)
	}
	if got.Verdict != DoctorFail {
		t.Fatalf("verdict = %q, want fail for observed downstream 500; hops=%+v reasons=%v", got.Verdict, got.Hops, got.Reasons)
	}
}

func TestDoctorCannotPassWithTruncatedCapture(t *testing.T) {
	s, _ := newDoctorSingleTarget(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("x", proxy.CaptureLimit+1))
	}))
	_, got, _ := runDoctor(t, s, DoctorRequest{Target: "entry", Path: "/large", TimeoutMs: 1000})
	if got.Verdict != DoctorInconclusive {
		t.Fatalf("verdict = %q, want inconclusive for truncated evidence; hops=%+v reasons=%v", got.Verdict, got.Hops, got.Reasons)
	}
	var sawTruncated bool
	for _, h := range got.Hops {
		sawTruncated = sawTruncated || containsString(h.Quality, "body-truncated")
	}
	if !sawTruncated {
		t.Fatalf("truncated capture not surfaced: %+v", got.Hops)
	}
}

func TestDoctorFailsHTTPErrorAndDoesNotFollowRedirect(t *testing.T) {
	t.Run("HTTP error", func(t *testing.T) {
		s, _ := newDoctorSingleTarget(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "broken", http.StatusServiceUnavailable)
		}))
		_, got, _ := runDoctor(t, s, DoctorRequest{Target: "entry", Path: "/probe", TimeoutMs: 1000})
		if got.Verdict != DoctorFail || got.HTTPStatus != http.StatusServiceUnavailable {
			t.Fatalf("result = %+v", got)
		}
	})

	t.Run("redirect", func(t *testing.T) {
		var followed atomic.Bool
		destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			followed.Store(true)
		}))
		t.Cleanup(destination.Close)
		s, _ := newDoctorSingleTarget(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, destination.URL+"/escaped", http.StatusFound)
		}))
		_, got, _ := runDoctor(t, s, DoctorRequest{Target: "entry", Path: "/probe", TimeoutMs: 1000})
		if followed.Load() {
			t.Fatal("doctor followed a redirect")
		}
		if got.Verdict != DoctorFail || got.HTTPStatus != http.StatusFound {
			t.Fatalf("result = %+v", got)
		}
	})
}

func TestDoctorTimeoutCancelsOpenStreamAndCleansSession(t *testing.T) {
	s, sessions := newDoctorSingleTarget(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "chunk")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	started := time.Now()
	_, got, _ := runDoctor(t, s, DoctorRequest{Target: "entry", Path: "/stream", TimeoutMs: 30})
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("timeout returned after %s", elapsed)
	}
	if got.Verdict != DoctorFail || got.ProbeError == "" {
		t.Fatalf("timeout result = %+v", got)
	}
	if sessions.Get(got.SessionID) != nil {
		t.Fatal("timeout left its temporary session active")
	}
}

func TestDoctorRejectsMalformedInputsBeforeTraffic(t *testing.T) {
	s, _ := newDoctorSingleTarget(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("invalid doctor input sent probe traffic")
	}))
	s.Cfg.Services["direct"] = config.Service{Port: 1234}
	longTarget := strings.Repeat("x", 129)
	s.Cfg.Services[longTarget] = s.Cfg.Services["entry"]
	manyExpected, _ := json.Marshal(DoctorRequest{Target: "entry", Path: "/", Expect: make([]string, 65)})
	var manyBody map[string]any
	_ = json.Unmarshal(manyExpected, &manyBody)
	names := make([]string, 65)
	for i := range names {
		names[i] = "entry"
	}
	manyBody["expect"] = names
	manyExpected, _ = json.Marshal(manyBody)
	tests := []struct {
		name string
		body string
	}{
		{name: "malformed JSON", body: `{`},
		{name: "unknown field", body: `{"target":"entry","path":"/","retry":true}`},
		{name: "missing target", body: `{"path":"/"}`},
		{name: "missing path", body: `{"target":"entry"}`},
		{name: "absolute URL", body: `{"target":"entry","path":"http://example.test/"}`},
		{name: "authority path", body: `{"target":"entry","path":"//example.test/"}`},
		{name: "fragment", body: `{"target":"entry","path":"/ok#fragment"}`},
		{name: "negative timeout", body: `{"target":"entry","path":"/","timeoutMs":-1}`},
		{name: "excessive timeout", body: `{"target":"entry","path":"/","timeoutMs":30001}`},
		{name: "overflowing timeout", body: fmt.Sprintf(`{"target":"entry","path":"/","timeoutMs":%d}`, int64(math.MaxInt64))},
		{name: "excessive path", body: fmt.Sprintf(`{"target":"entry","path":"/%s"}`, strings.Repeat("p", 2048))},
		{name: "excessive target", body: fmt.Sprintf(`{"target":%q,"path":"/"}`, longTarget)},
		{name: "too many expectations", body: string(manyExpected)},
		{name: "unknown target", body: `{"target":"missing","path":"/"}`},
		{name: "non-proxied target", body: `{"target":"direct","path":"/"}`},
		{name: "unknown expectation", body: `{"target":"entry","path":"/","expect":["missing"]}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/doctor", strings.NewReader(tc.body))
			rr := httptest.NewRecorder()
			s.handleDoctor(rr, req)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
			}
		})
	}
}

func TestDoctorTimeoutMetadataDoesNotDiscloseQuery(t *testing.T) {
	s, _ := newDoctorSingleTarget(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "chunk")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	_, got, _ := runDoctor(t, s, DoctorRequest{Target: "entry", Path: "/stream?api_key=top-secret", TimeoutMs: 30})
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("top-secret")) || bytes.Contains(encoded, []byte("api_key")) {
		t.Fatalf("doctor result disclosed query in error metadata: %s", encoded)
	}
}

func TestDoctorExecutesQueryButOmitsItFromMetadata(t *testing.T) {
	var query string
	s, _ := newDoctorSingleTarget(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		w.WriteHeader(http.StatusNoContent)
	}))
	_, got, _ := runDoctor(t, s, DoctorRequest{Target: "entry", Path: "/probe?token=secret", TimeoutMs: 1000})
	if query != "token=secret" {
		t.Fatalf("upstream query = %q", query)
	}
	if got.Path != "/probe" {
		t.Fatalf("response path = %q", got.Path)
	}
	for _, h := range got.Hops {
		if strings.Contains(h.Path, "secret") || strings.Contains(h.Path, "?") {
			t.Fatalf("hop metadata disclosed query: %+v", h)
		}
	}
}

func TestDoctorCanProbeConfiguredGateway(t *testing.T) {
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(app.Close)
	rec := proxy.NewRecorder(proxy.RecorderOpts{Ring: 32})
	px := proxy.New(rec)
	t.Cleanup(px.Close)
	addr, err := px.Serve(proxy.Target{Name: "public", Listen: "127.0.0.1:0", Upstream: app.URL})
	if err != nil {
		t.Fatal(err)
	}
	sessions := proxy.NewSessionManager(px, rec, []string{"public"})
	t.Cleanup(sessions.Close)
	s := &server{Deps: Deps{
		Cfg: &config.Config{Gateways: map[string]config.Gateway{"public": {Port: doctorPort(t, addr)}}},
		Rec: rec, Sessions: sessions,
	}}
	_, got, _ := runDoctor(t, s, DoctorRequest{Target: "public", Path: "/ready", TimeoutMs: 1000})
	if got.Verdict != DoctorPass || !containsString(got.ObservedTargets, "public") {
		t.Fatalf("gateway result = %+v", got)
	}
}

func TestDoctorCallerCancellationReturnsPromptlyAndCleansSession(t *testing.T) {
	s, sessions := newDoctorSingleTarget(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	body, _ := json.Marshal(DoctorRequest{Target: "entry", Path: "/wait", TimeoutMs: 1000})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodPost, "/api/doctor", bytes.NewReader(body)).WithContext(ctx)
	rr := httptest.NewRecorder()
	started := time.Now()
	s.handleDoctor(rr, req)
	if elapsed := time.Since(started); elapsed > 300*time.Millisecond {
		t.Fatalf("canceled request returned after %s", elapsed)
	}
	var got DoctorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode result: %v; body=%s", err, rr.Body.String())
	}
	if got.ProbeError != "probe canceled" {
		t.Fatalf("probe error = %q", got.ProbeError)
	}
	if sessions.Get(got.SessionID) != nil {
		t.Fatal("canceled caller left its temporary session active")
	}
}

func TestDoctorCannotPassHopWithoutSpanIdentity(t *testing.T) {
	rec := proxy.NewRecorder(proxy.RecorderOpts{Ring: 16})
	px := proxy.New(rec)
	t.Cleanup(px.Close)
	sessions := proxy.NewSessionManager(px, rec, nil)
	t.Cleanup(sessions.Close)
	root := trace.NewCtx()
	sessionID := "doctor-" + root.TraceID
	ses, err := sessions.Start(sessionID, doctorEdgeTarget, "http://127.0.0.1:1", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	rec.Record(trace.Hop{
		TraceID: root.TraceID, ParentSpanID: root.SpanID, Session: sessionID,
		To: "entry", Method: http.MethodGet, Path: "/", Status: http.StatusOK,
		T: trace.Timings{Start: time.Now(), DoneMs: 1},
	})
	deadline := time.Now().Add(time.Second)
	for len(ses.Hops()) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	sessions.End(sessionID)
	got := classifyDoctor(DoctorRequest{Target: "entry", Path: "/"}, root, sessionID, http.StatusOK, time.Millisecond, nil, ses, 0, 0)
	if got.Verdict == DoctorPass || got.Propagation.TraceContext {
		t.Fatalf("span-less hop passed context gate: %+v", got)
	}
}
