package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

// ensembleAPI is an httptest server that answers exactly the four routes
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

	_, err := NewClient(srv.URL).StartSession(context.Background(), "run-1", "bff")
	if err == nil {
		t.Fatal("StartSession against a 404 returned no error")
	}
	if !strings.Contains(err.Error(), `entry service "bff" not found`) {
		t.Fatalf("error = %q, want the server's own message", err)
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
