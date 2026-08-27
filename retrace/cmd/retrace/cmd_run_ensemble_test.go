package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/retrace/capture"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

// ensembleAPI is an httptest server that answers exactly the five routes
// retrace uses, in exactly the shapes ensemble/server/routes.go serves
// them. retrace must NOT import ensemble — not in production code and not
// in a test (a test-only import is still a require + replace in
// retrace/go.mod, and Design §1 promises a team can adopt retrace in CI
// without ever running ensemble). What has to be proven here is that
// client.go speaks ensemble's WIRE contract, which is an HTTP-level
// property, so an HTTP-level fake proves it. The shapes below were read off
// routes.go's handleSessionStart / handleSessionEnd / handleSessionHops; if
// ensemble ever changes them, THIS is the test that must be updated,
// deliberately.
type ensembleAPI struct {
	*httptest.Server
	edge *httptest.Server

	mu      sync.Mutex
	started []string // session ids POST /api/sessions was called with
	ended   []string // session ids DELETE /api/sessions/{id} was called with
	// status is the raw GET /api/status body. Empty means the route reports a
	// stack with nothing in it — the shape an ensemble predating `version:`
	// serves, and the one every other test in this file exercises.
	status string
}

func newEnsembleAPI(t *testing.T, hops []trace.Hop) *ensembleAPI {
	t.Helper()
	a := &ensembleAPI{}
	// A real listener behind the fake control plane: `edgeAddr` is what the
	// test command actually fetches through, so it has to answer.
	a.edge = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(a.edge.Close)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSONResponse(w, map[string]any{"ok": true, "version": "test"})
	})
	// The stack fingerprint, read once at session start. A control plane that
	// does not serve this route leaves the run with no stack record rather
	// than failing it.
	mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		body := a.status
		a.mu.Unlock()
		if body == "" {
			body = `{"services":[{"name":"bff"}],"readiness":{"state":"ready"}}`
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})
	mux.HandleFunc("POST /api/sessions", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ ID, Entry string }
		_ = json.NewDecoder(r.Body).Decode(&req)
		a.mu.Lock()
		a.started = append(a.started, req.ID)
		a.mu.Unlock()
		writeJSONResponse(w, map[string]any{
			"id":       req.ID,
			"edgeAddr": strings.TrimPrefix(a.edge.URL, "http://"),
		})
	})
	// NDJSON, not a JSON array — and written with the same encoder ensemble
	// itself uses, so the bytes are not hand-rolled.
	mux.HandleFunc("GET /api/sessions/{id}/hops", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		wr := trace.NewWriter(w)
		for _, h := range hops {
			_ = wr.Write(h)
		}
	})
	mux.HandleFunc("DELETE /api/sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		a.mu.Lock()
		a.ended = append(a.ended, id)
		a.mu.Unlock()
		writeJSONResponse(w, map[string]any{
			"id": id, "hops": len(hops), "verdict": "ok", "reasons": []string{},
		})
	})
	a.Server = httptest.NewServer(mux)
	t.Cleanup(a.Server.Close)
	return a
}

func writeJSONResponse(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// chainHops is the two-layer recording this task exists to produce: one
// client-edge call (From == "") and one provider-to-provider call, sharing
// a trace id. hops.jsonl is the superset; wire.jsonl is the From=="" subset.
func chainHops() []trace.Hop {
	edge := trace.Hop{
		Schema: trace.SchemaVersion, Seq: 1, TraceID: "t-1",
		To: "edge", Method: "GET", Path: "/cart", Status: 200,
	}
	inner := trace.Hop{
		Schema: trace.SchemaVersion, Seq: 2, TraceID: "t-1",
		From: "edge", To: "bff", Method: "GET", Path: "/cart/items", Status: 200,
	}
	return []trace.Hop{edge, inner}
}

func TestRunAttachedRecordsTheFullChainAndSplitsWireFromHops(t *testing.T) {
	api := newEnsembleAPI(t, chainHops())
	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, "app: web\nentry: bff\n")

	args := append([]string{"run", "--flow", "checkout", "--ensemble", api.URL}, selfCmd(t, "TestHelperFetchesThroughProxy")...)
	res := runRetrace(t, bin, cwd, "fetch", args...)
	if res.code != 0 {
		t.Fatalf("exit = %d\nstdout: %s\nstderr: %s", res.code, res.stdout, res.stderr)
	}

	m := onlyManifest(t, cwd, "web", "checkout")
	if m.Mode != runs.ModeEnsemble {
		t.Fatalf("manifest.mode = %q, want %q\nstderr: %s", m.Mode, runs.ModeEnsemble, res.stderr)
	}
	if m.Hops == nil || m.Hops.Calls != 2 {
		t.Fatalf("manifest.hops = %+v, want 2 calls", m.Hops)
	}
	if m.Wire.Calls != 1 {
		t.Fatalf("manifest.wire.calls = %d, want 1 (the client edge alone)", m.Wire.Calls)
	}

	root := runs.RunsRoot(cwd)
	p, err := runs.PathsFor(root, "web", "checkout", m.RunID)
	if err != nil {
		t.Fatalf("PathsFor: %v", err)
	}
	full, _, err := runs.ReadHops(p.HopsPath)
	if err != nil || len(full) != 2 {
		t.Fatalf("hops.jsonl = %d hop(s) (%v), want the full chain of 2", len(full), err)
	}
	wire, _, err := runs.ReadHops(p.WirePath)
	if err != nil || len(wire) != 1 || wire[0].From != "" || wire[0].To != "edge" {
		t.Fatalf("wire.jsonl = %+v (%v), want only the From==\"\" client-edge hop", wire, err)
	}

	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.started) != 1 || api.started[0] != m.RunID {
		t.Fatalf("POST /api/sessions ids = %v, want exactly [%s]", api.started, m.RunID)
	}
	if len(api.ended) != 1 || api.ended[0] != m.RunID {
		t.Fatalf("DELETE /api/sessions ids = %v, want exactly [%s]", api.ended, m.RunID)
	}
}

// TestRunAttachedWithNoTrafficIsNeverProxyNeverReached is Finding 1's
// regression test, and it MUST go through cmdRun's real production path —
// requestsSeenForTrust — not call capture.Assess directly with a
// hand-written RequestsSeen: -1. The two tests that "covered" this before
// the fix (TestAttachedZeroRequestsSeenNeverReadsAsProxyNeverReached in
// package capture, and the "zero calls, reachability unknown" table case)
// both construct AssessInput{RequestsSeen: -1} by hand — a value nothing in
// the production tree ever constructed, because the only production call
// site (assessTrust in cmd_run.go) hardcoded ProxyConfigured: true and
// passed s.RequestsSeen() raw. In attached mode s.rec is nil, so
// RequestsSeen() counts marker-door hits ONLY, and a healthy attached flow
// that posts no markers — this test's "true" command — returned 0, not -1.
// Before the fix, that verdicted this run `broken`/proxy-never-reached and
// Task 10 would have quarantined it: a perfectly good attached recording,
// accused of misconfiguring a base URL retrace does not even own in this
// mode.
//
// This is deliberately NOT the earlier "no traffic" fixture reused from
// TestRunBannersANonOkVerdict: that one is standalone, where RequestsSeen
// raw IS the reachability signal and `broken` is the CORRECT verdict.
// Attached is the mode where 0 must never be read as "verified and zero".
func TestRunAttachedWithNoTrafficIsNeverProxyNeverReached(t *testing.T) {
	api := newEnsembleAPI(t, nil) // no hops at all — ensemble drained nothing
	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, "app: web\nentry: bff\n")

	// "true" never dials the edge and never posts a marker, so
	// s.RequestsSeen() (marker-door hits only, in attached mode) is 0 —
	// exactly the value this finding is about.
	args := []string{"run", "--flow", "checkout", "--app", "web", "--ensemble", api.URL, "--", "true"}
	res := runRetrace(t, bin, cwd, "", args...)
	if res.code != 0 {
		t.Fatalf("exit = %d, want 0 (the test command itself succeeded)\nstdout: %s\nstderr: %s", res.code, res.stdout, res.stderr)
	}

	m := onlyManifest(t, cwd, "web", "checkout")
	if m.Mode != runs.ModeEnsemble {
		t.Fatalf("manifest.mode = %q, want %q — this test only proves what it claims to in attached mode", m.Mode, runs.ModeEnsemble)
	}
	if m.Capture.Status == trace.VerdictBroken {
		t.Fatalf("capture.status = broken — a healthy attached run with no marker traffic must never read as proxy-never-reached (attached mode cannot verify reachability, it can only prove unknown): %+v", m.Capture)
	}
	for _, r := range m.Capture.Reasons {
		if r.Code == "proxy-never-reached" {
			t.Fatalf("capture.reasons contains proxy-never-reached: %+v", m.Capture)
		}
	}
	// The honest reading of "attached, zero marker hits" is degraded/no-calls
	// (unknown reachability), not ok — RequestsSeen: -1 still enters the
	// zero-Hops branch, it just lands in the "unknown" case rather than the
	// "confirmed absent" one.
	if m.Capture.Status != trace.VerdictDegraded {
		t.Fatalf("capture.status = %q, want %q: %+v", m.Capture.Status, trace.VerdictDegraded, m.Capture)
	}
	if !strings.Contains(res.stderr, "capture-trust:") || !strings.Contains(res.stderr, "degraded") {
		t.Fatalf("stderr does not banner the degraded capture-trust verdict:\n%s", res.stderr)
	}
}

// The single most likely integration bug, and the one a hand-written fake
// could paper over: GET /api/sessions/{id}/hops is NDJSON, not a JSON
// array. A client written against json.Unmarshal of an array fails here,
// which is the point. The blank line is deliberate — trace.NewReader skips
// it, and a naive bufio+Unmarshal loop would not.
func TestSessionHopsParsesNdjsonNotAJsonArray(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		wr := trace.NewWriter(w)
		for i := 1; i <= 3; i++ {
			_ = wr.Write(trace.Hop{To: fmt.Sprintf("svc-%d", i), Method: "GET", Status: 200})
			if i == 2 {
				_, _ = w.Write([]byte("\n"))
			}
		}
	}))
	defer srv.Close()

	hops, err := NewClient(srv.URL).SessionHops(context.Background(), "run-1")
	if err != nil {
		t.Fatalf("SessionHops: %v", err)
	}
	if len(hops) != 3 {
		t.Fatalf("SessionHops returned %d hop(s), want 3 — the body is NDJSON, not a JSON array", len(hops))
	}
	for i, h := range hops {
		if want := fmt.Sprintf("svc-%d", i+1); h.To != want {
			t.Fatalf("hops[%d].To = %q, want %q (order must be preserved)", i, h.To, want)
		}
	}
}

// The server's {"error":"..."} convention, translated. 409 on an active id
// and 404 on an unknown entry are the two failures a real run hits.
func TestClientTranslatesTheServersErrorConvention(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"entry service \"bff\" not found"}`))
	}))
	defer srv.Close()

	_, err := NewClient(srv.URL).StartSession(context.Background(), capture.SessionRequest{ID: "run-1", Entry: "bff"})
	if err == nil {
		t.Fatal("StartSession against a 404 returned no error")
	}
	if !strings.Contains(err.Error(), `entry service "bff" not found`) {
		t.Fatalf("error = %q, want the server's own message", err)
	}
}

// Major 5: the zero-value clause. A 200 whose body omits edgeAddr is the
// wire shape of "ensemble accepted the session but told us nothing usable" —
// treating an empty edgeAddr as permissive would produce ProxyURL
// "http://", and every request from the test command would die with
// "http: no Host in request URL" while the manifest still said mode:
// ensemble.
func TestStartSessionRejectsAResponseWithNoEdgeAddr(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSONResponse(w, map[string]any{"id": "run-1"})
	}))
	defer srv.Close()

	_, err := NewClient(srv.URL).StartSession(context.Background(), capture.SessionRequest{ID: "run-1", Entry: "bff"})
	if err == nil {
		t.Fatal("StartSession accepted a response with no edgeAddr")
	}
}

// An ensemble predating proxy.host support silently ignores an unknown
// "host" field in the request body and answers on its own default
// (127.0.0.1) — a version-skew shape, not a wire error, so it needs its own
// check rather than falling out of the no-edgeAddr case above. Left
// unchecked, this surfaces downstream only as an unexplained 401 from
// whatever URL-bound auth validator proxy.host was set for (design.md
// §6.1.2); StartSession is the one place that has both the request and the
// answer, so it is the one place that can name the real cause.
func TestStartSessionDiagnosesAnEnsembleThatIgnoresProxyHost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSONResponse(w, map[string]any{"id": "run-1", "edgeAddr": "127.0.0.1:54321"})
	}))
	defer srv.Close()

	_, err := NewClient(srv.URL).StartSession(context.Background(), capture.SessionRequest{ID: "run-1", Entry: "bff", Host: "localhost"})
	if err == nil {
		t.Fatal("StartSession with a mismatched edge host returned no error")
	}
	if !strings.Contains(err.Error(), "does not support proxy.host") {
		t.Errorf("error = %q, want it to diagnose the version skew", err)
	}
}

// The matching positive case: when ensemble's edge answers with the same
// host that was requested, StartSession must not treat that as a mismatch.
func TestStartSessionAcceptsAMatchingProxyHost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSONResponse(w, map[string]any{"id": "run-1", "edgeAddr": "localhost:54321"})
	}))
	defer srv.Close()

	addr, err := NewClient(srv.URL).StartSession(context.Background(), capture.SessionRequest{ID: "run-1", Entry: "bff", Host: "localhost"})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	if addr != "localhost:54321" {
		t.Errorf("edgeAddr = %q, want %q", addr, "localhost:54321")
	}
}

// The port-conflict mirror of TestStartSessionDiagnosesAnEnsembleThatIgnoresProxyHost:
// an ensemble predating proxy.port support silently ignores an unknown
// "port" field and answers on its own default port instead.
func TestStartSessionDiagnosesAnEnsembleThatIgnoresProxyPort(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSONResponse(w, map[string]any{"id": "run-1", "edgeAddr": "127.0.0.1:54321"})
	}))
	defer srv.Close()

	_, err := NewClient(srv.URL).StartSession(context.Background(), capture.SessionRequest{ID: "run-1", Entry: "bff", Port: 4000})
	if err == nil {
		t.Fatal("StartSession with a mismatched edge port returned no error")
	}
	if !strings.Contains(err.Error(), "does not support proxy_port") {
		t.Errorf("error = %q, want it to diagnose the version skew", err)
	}
}

// The matching positive case: when ensemble's edge answers on the same
// port that was requested, StartSession must not treat that as a mismatch.
func TestStartSessionAcceptsAMatchingProxyPort(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSONResponse(w, map[string]any{"id": "run-1", "edgeAddr": "127.0.0.1:4000"})
	}))
	defer srv.Close()

	addr, err := NewClient(srv.URL).StartSession(context.Background(), capture.SessionRequest{ID: "run-1", Entry: "bff", Port: 4000})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	if addr != "127.0.0.1:4000" {
		t.Errorf("edgeAddr = %q, want %q", addr, "127.0.0.1:4000")
	}
}

// --- Major 1, full stack: bounded calls through the real net/http client ---
//
// These reproduce the review's probes A and B: a handler that accepts the
// TCP connection and never writes a response byte. The capture-package
// tests (ensemble_test.go) pin the same bound through an in-process fake;
// these prove it end to end through client.go's actual http.Client.
//
// Deliberately NOT httptest.NewServer, and deliberately NOT
// `<-r.Context().Done()` to model "never answers": httptest.Server.Close
// waits (via a WaitGroup) for every outstanding handler to return before it
// ever force-closes a connection, and canceling a request's context is not
// a reliable way to unblock a handler that never touches the connection
// itself (measured: even a forced http.Server.Close() left the handler
// goroutine, and the leaked connection, alive — accumulating across
// `-race -count=20` until an unrelated later test starved on the pile-up
// and hung the whole package). wedgedServer instead hands the handler an
// explicit release channel that Cleanup closes directly, so "never
// answers" lasts exactly as long as the test does — no dependence on
// net/http's connection/context plumbing at all.
func wedgedServer(t *testing.T, register func(mux *http.ServeMux, release <-chan struct{})) string {
	t.Helper()
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	mux := http.NewServeMux()
	register(mux, release)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return "http://" + ln.Addr().String()
}

func TestStartAttachedDoesNotHangOnAWedgedControlPlane(t *testing.T) {
	url := wedgedServer(t, func(mux *http.ServeMux, release <-chan struct{}) {
		mux.HandleFunc("POST /api/sessions", func(w http.ResponseWriter, r *http.Request) {
			<-release // accepts the connection, never answers
		})
	})

	start := time.Now()
	_, err := capture.StartAttached(capture.Options{Cwd: t.TempDir(), App: "web", Flow: "checkout"}, NewClient(url), "bff")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("StartAttached against a wedged control plane returned nil error; want it bounded and erroring")
	}
	if elapsed > 8*time.Second {
		t.Fatalf("StartAttached took %s against a wedged control plane (a POST that never answers); it must be bounded by a context deadline, not context.Background()", elapsed)
	}
}

func TestDrainDoesNotHangOnAWedgedControlPlane(t *testing.T) {
	url := wedgedServer(t, func(mux *http.ServeMux, release <-chan struct{}) {
		mux.HandleFunc("POST /api/sessions", func(w http.ResponseWriter, r *http.Request) {
			writeJSONResponse(w, map[string]any{"id": "run-1", "edgeAddr": "127.0.0.1:1"})
		})
		mux.HandleFunc("GET /api/sessions/{id}/hops", func(w http.ResponseWriter, r *http.Request) {
			<-release // accepts the connection, never answers
		})
	})

	s, err := capture.StartAttached(capture.Options{Cwd: t.TempDir(), App: "web", Flow: "checkout"}, NewClient(url), "bff")
	if err != nil {
		t.Fatalf("StartAttached: %v", err)
	}
	defer func() { _ = s.Close() }()

	start := time.Now()
	drainErr := s.Drain(context.Background())
	elapsed := time.Since(start)
	if drainErr == nil {
		t.Fatal("Drain against a wedged hops endpoint returned nil error; want it bounded and erroring")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("Drain took %s against a wedged hops endpoint; the drain window must bind during a poll, not only between polls", elapsed)
	}
}

// --- zero-value pins (global-constraints.md, both clauses) ---
//
// "could not reach ensemble" and "ensemble said everything is fine" must
// never be represented by values that compare equal, or a run against a
// broken topology records as a clean full-chain capture. The manifest's
// mode field is where that distinction lands: an attach that did not happen
// is `standalone`, always, and hops.jsonl is absent rather than empty.

func TestRunFallsBackToStandaloneWhenEnsembleIsNotAnswering(t *testing.T) {
	// A closed listener's address: nothing answers, and the address is not
	// reused because httptest hands out a fresh port per server.
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, "app: web\nentry: bff\nupstream: "+upstream.URL+"\n")

	args := append([]string{"run", "--flow", "checkout", "--ensemble", deadURL}, selfCmd(t, "TestHelperFetchesThroughProxy")...)
	res := runRetrace(t, bin, cwd, "fetch", args...)
	if res.code != 0 {
		t.Fatalf("exit = %d\nstderr: %s", res.code, res.stderr)
	}
	if !strings.Contains(res.stderr, "is not answering") {
		t.Fatalf("stderr = %q; a silent downgrade is how a partial recording gets believed", res.stderr)
	}

	m := onlyManifest(t, cwd, "web", "checkout")
	if m.Mode != runs.ModeStandalone {
		t.Fatalf("manifest.mode = %q after a failed attach, want %q", m.Mode, runs.ModeStandalone)
	}
	// nil, not &Counts{0}: "no chain was recorded" and "the chain was
	// recorded and was empty" are different facts.
	if m.Hops != nil {
		t.Fatalf("manifest.hops = %+v after a failed attach, want nil", m.Hops)
	}
}

// The same pin one layer deeper: ensemble ANSWERS /api/health but refuses
// the session (404 unknown entry, 409 active id, 400 no proxy port). The
// health check alone is not proof of an attached capture, so a manifest
// that says `ensemble` here would claim a full-chain recording that was
// never made.
func TestRunFallsBackToStandaloneWhenEnsembleRefusesTheSession(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSONResponse(w, map[string]any{"ok": true, "version": "test"})
	})
	mux.HandleFunc("POST /api/sessions", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"entry service \"bff\" not found"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, "app: web\nentry: bff\nupstream: "+upstream.URL+"\n")

	args := append([]string{"run", "--flow", "checkout", "--ensemble", srv.URL}, selfCmd(t, "TestHelperFetchesThroughProxy")...)
	res := runRetrace(t, bin, cwd, "fetch", args...)
	if res.code != 0 {
		t.Fatalf("exit = %d\nstderr: %s", res.code, res.stderr)
	}
	if !strings.Contains(res.stderr, "refused the session") {
		t.Fatalf("stderr = %q, want an explicit note that the attach was refused", res.stderr)
	}
	m := onlyManifest(t, cwd, "web", "checkout")
	if m.Mode != runs.ModeStandalone || m.Hops != nil {
		t.Fatalf("manifest mode=%q hops=%+v after a refused session, want standalone/nil", m.Mode, m.Hops)
	}
}

// A control plane that answers 200 with a body that does not say ok:true is
// not healthy. `ok` false is the zero value of the decoded struct, so a
// Health that only checked the status code would attach to anything that
// serves 200 on /api/health — including an unrelated dev server.
func TestHealthRejectsA200ThatDoesNotSayOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSONResponse(w, map[string]any{"version": "test"})
	}))
	defer srv.Close()
	if err := NewClient(srv.URL).Health(context.Background()); err == nil {
		t.Fatal("Health accepted a 200 whose body never said ok:true")
	}
}

// --no-ensemble is the escape hatch: a healthy control plane is present and
// the config names an entry, and retrace still records the client edge only.
func TestNoEnsembleForcesStandaloneEvenWhenEnsembleIsUp(t *testing.T) {
	api := newEnsembleAPI(t, chainHops())
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, "app: web\nentry: bff\nupstream: "+upstream.URL+"\n")

	args := append([]string{"run", "--flow", "checkout", "--ensemble", api.URL, "--no-ensemble"}, selfCmd(t, "TestHelperFetchesThroughProxy")...)
	res := runRetrace(t, bin, cwd, "fetch", args...)
	if res.code != 0 {
		t.Fatalf("exit = %d\nstderr: %s", res.code, res.stderr)
	}
	m := onlyManifest(t, cwd, "web", "checkout")
	if m.Mode != runs.ModeStandalone {
		t.Fatalf("manifest.mode = %q with --no-ensemble, want %q", m.Mode, runs.ModeStandalone)
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.started) != 0 {
		t.Fatalf("--no-ensemble still registered sessions: %v", api.started)
	}
}

// TestAnAttachedRunRecordsTheStackItWasRecordedAgainst is the end of the
// chain the fingerprint travels: ensemble reports it on /api/status, the
// client decodes it, the session holds it, and the manifest keeps it. A
// value that is read correctly and then dropped before the manifest is
// written is worth exactly nothing, and every stage above this one passes
// its own tests while that happens.
func TestAnAttachedRunRecordsTheStackItWasRecordedAgainst(t *testing.T) {
	api := newEnsembleAPI(t, chainHops())
	api.status = `{"services":[{"name":"bff","version":"abc123"}],` +
		`"seed":{"name":"baseline","appliedAt":"2026-08-01T12:00:00Z"}}`
	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, "app: web\nentry: bff\n")

	args := append([]string{"run", "--flow", "checkout", "--ensemble", api.URL}, selfCmd(t, "TestHelperFetchesThroughProxy")...)
	res := runRetrace(t, bin, cwd, "fetch", args...)
	if res.code != 0 {
		t.Fatalf("exit = %d\nstdout: %s\nstderr: %s", res.code, res.stdout, res.stderr)
	}

	m := onlyManifest(t, cwd, "web", "checkout")
	if m.Stack == nil {
		t.Fatal("the manifest records no stack for a run against a control plane that reported one")
	}
	if m.Stack.Services["bff"] != "abc123" {
		t.Errorf("stack services = %v, want bff=abc123", m.Stack.Services)
	}
	if m.Stack.Seed == nil || m.Stack.Seed.Name != "baseline" {
		t.Errorf("stack seed = %+v, want baseline", m.Stack.Seed)
	}
}

// TestAnAttachedRunAgainstAnUnfingerprintedStackRecordsNone is the other
// half: nil, never an empty stack. An empty one compares equal to every
// other run that recorded nothing, which would turn two unfingerprinted runs
// into positive evidence that the backend did not move.
func TestAnAttachedRunAgainstAnUnfingerprintedStackRecordsNone(t *testing.T) {
	api := newEnsembleAPI(t, chainHops())
	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, "app: web\nentry: bff\n")

	args := append([]string{"run", "--flow", "checkout", "--ensemble", api.URL}, selfCmd(t, "TestHelperFetchesThroughProxy")...)
	res := runRetrace(t, bin, cwd, "fetch", args...)
	if res.code != 0 {
		t.Fatalf("exit = %d\nstdout: %s\nstderr: %s", res.code, res.stdout, res.stderr)
	}

	if m := onlyManifest(t, cwd, "web", "checkout"); m.Stack != nil {
		t.Errorf("manifest stack = %+v, want nil", m.Stack)
	}
}
