package server_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/caribou-crew/ensemble/core/trace"
)

// writeHopsHistory drops hops into .ensemble/hops.jsonl under the test
// env's config dir — the exact file cmd_up.go's runUp opens as a rotating
// writer, and the only file handleTrafficHistory/handleSessionExport's
// disk half reads.
func writeHopsHistory(t *testing.T, e *testEnv, hops []trace.Hop) string {
	t.Helper()
	dir := filepath.Join(e.cfg.Dir, ".ensemble")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	path := filepath.Join(dir, "hops.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	defer f.Close()
	w := trace.NewWriter(f)
	for _, h := range hops {
		if err := w.Write(h); err != nil {
			t.Fatalf("write hop seq=%d: %v", h.Seq, err)
		}
	}
	return path
}

type historyResponse struct {
	Hops         []trace.Hop `json:"hops"`
	CorruptLines int         `json:"corruptLines"`
	HasMore      bool        `json:"hasMore"`
}

func getHistory(t *testing.T, e *testEnv, query string) historyResponse {
	t.Helper()
	resp, body := e.get(t, "/api/traffic/history"+query)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	var got historyResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v (body=%s)", err, body)
	}
	return got
}

// TestTrafficHistoryReachesPastTheRing guards the spec's headline
// scenario: hops.jsonl retains everything ever recorded, so a page whose
// seqs the in-memory ring has long since evicted is still reachable
// through the disk-backed endpoint.
func TestTrafficHistoryReachesPastTheRing(t *testing.T) {
	e := newTestEnv(t)
	var hops []trace.Hop
	for seq := uint64(1); seq <= 1200; seq++ {
		hops = append(hops, trace.Hop{Seq: seq, To: "x", Method: "GET", Path: "/p", Status: 200})
	}
	writeHopsHistory(t, e, hops)

	got := getHistory(t, e, "?before=1000&limit=100")
	if len(got.Hops) != 100 {
		t.Fatalf("got %d hops, want 100", len(got.Hops))
	}
	// Newest-first: seq 999 down to 900.
	if got.Hops[0].Seq != 999 || got.Hops[len(got.Hops)-1].Seq != 900 {
		t.Fatalf("range = [%d..%d], want [999..900]", got.Hops[0].Seq, got.Hops[len(got.Hops)-1].Seq)
	}
	if !got.HasMore {
		t.Error("hasMore = false, want true (hops 1-899 remain)")
	}
}

// TestTrafficHistoryNoFileIsEmptyPage covers the spec's "no history file"
// scenario: nothing recorded yet is a normal empty page, not an error.
func TestTrafficHistoryNoFileIsEmptyPage(t *testing.T) {
	e := newTestEnv(t)
	got := getHistory(t, e, "")
	if len(got.Hops) != 0 || got.CorruptLines != 0 || got.HasMore {
		t.Fatalf("got %+v, want an empty, unremarkable page", got)
	}
}

// TestTrafficHistoryFilters exercises errorsOnly/session/method/path/
// status, mirroring GET /api/traffic's own filter contract.
func TestTrafficHistoryFilters(t *testing.T) {
	e := newTestEnv(t)
	hops := []trace.Hop{
		{Seq: 1, To: "x", Method: "GET", Path: "/widgets", Status: 200, Session: "sess-a"},
		{Seq: 2, To: "x", Method: "POST", Path: "/widgets", Status: 500, Session: "sess-a"},
		{Seq: 3, To: "x", Method: "GET", Path: "/gadgets", Status: 404, Session: "sess-b"},
		{Seq: 4, To: "x", Method: "GET", Path: "/widgets", Status: 200, Err: "dial: refused", Session: "sess-b"},
	}
	writeHopsHistory(t, e, hops)

	cases := []struct {
		name    string
		query   string
		wantSeq []uint64 // newest-first, as the endpoint returns them
	}{
		{"errorsOnly", "?errorsOnly=true", []uint64{4, 3, 2}},
		{"session", "?session=sess-a", []uint64{2, 1}},
		{"method", "?method=post", []uint64{2}}, // case-insensitive
		{"path substring", "?path=gadget", []uint64{3}},
		{"status exact", "?status=200", []uint64{4, 1}},
		{"combined", "?session=sess-a&status=200", []uint64{1}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := getHistory(t, e, c.query)
			if len(got.Hops) != len(c.wantSeq) {
				t.Fatalf("got %d hops, want %d (%+v)", len(got.Hops), len(c.wantSeq), got.Hops)
			}
			for i, h := range got.Hops {
				if h.Seq != c.wantSeq[i] {
					t.Errorf("hops[%d].Seq = %d, want %d", i, h.Seq, c.wantSeq[i])
				}
			}
		})
	}
}

// TestTrafficHistorySkipsCorruptLines guards the "corrupt lines skipped
// and counted, never fail the request" contract: a malformed line
// anywhere in the file (a crash mid-write, manual edit) must not hide
// every hop after it, and must be reported rather than silently eaten.
func TestTrafficHistorySkipsCorruptLines(t *testing.T) {
	e := newTestEnv(t)
	dir := filepath.Join(e.cfg.Dir, ".ensemble")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, "hops.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	w := trace.NewWriter(f)
	mustWrite := func(h trace.Hop) {
		t.Helper()
		if err := w.Write(h); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	mustWrite(trace.Hop{Seq: 1, To: "x", Status: 200})
	if _, err := f.WriteString("{not valid json\n"); err != nil {
		t.Fatalf("write garbage: %v", err)
	}
	mustWrite(trace.Hop{Seq: 2, To: "x", Status: 200})
	if _, err := f.WriteString("also not json\n"); err != nil {
		t.Fatalf("write garbage: %v", err)
	}
	mustWrite(trace.Hop{Seq: 3, To: "x", Status: 200})
	f.Close()

	got := getHistory(t, e, "")
	if len(got.Hops) != 3 {
		t.Fatalf("got %d hops, want 3 (corrupt lines shouldn't hide neighbors): %+v", len(got.Hops), got.Hops)
	}
	if got.CorruptLines != 2 {
		t.Errorf("corruptLines = %d, want 2", got.CorruptLines)
	}
}

// --- whole-session HAR export ---

func TestSessionExportUnionOfRingAndDisk(t *testing.T) {
	e := newTestEnv(t)

	// Two hops still live in the ring, assigned real seqs (1, 2) by the
	// recorder's own counter...
	h1 := e.rec.Record(trace.Hop{To: "other", Session: "run-1", TraceID: "t1", Method: "GET", Path: "/a", Status: 200})
	h2 := e.rec.Record(trace.Hop{To: "other", Session: "run-1", TraceID: "t2", Method: "GET", Path: "/b", Status: 200})
	// ...a third, from the same session, is disk-only (simulating a hop
	// the ring has already rolled past) — its seq deliberately doesn't
	// collide with the ring's own counter above.
	diskOnly := trace.Hop{Seq: 100, To: "other", Session: "run-1", TraceID: "t0", Method: "GET", Path: "/z", Status: 200}
	// A hop from a different session must never leak into the export.
	writeHopsHistory(t, e, []trace.Hop{diskOnly, {Seq: 101, To: "other", Session: "run-2", Method: "GET", Path: "/nope", Status: 200}})

	resp, body := e.get(t, "/api/sessions/run-1/export?format=har")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	var got trace.Har
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	want := trace.ToHar([]trace.Hop{h1, h2, diskOnly}) // chronological (ascending seq)
	if len(got.Log.Entries) != len(want.Log.Entries) {
		t.Fatalf("got %d entries, want %d", len(got.Log.Entries), len(want.Log.Entries))
	}
	for i := range want.Log.Entries {
		if got.Log.Entries[i].Request.URL != want.Log.Entries[i].Request.URL {
			t.Errorf("entry[%d].Request.URL = %q, want %q", i, got.Log.Entries[i].Request.URL, want.Log.Entries[i].Request.URL)
		}
	}
}

func TestSessionExportUnknownFormatIs400(t *testing.T) {
	e := newTestEnv(t)
	resp, _ := e.get(t, "/api/sessions/run-1/export?format=csv")
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// TestSessionExportEmptySessionIsEmptyHar: an unknown/empty session id is
// an empty export, not an error — mirrors handleTraceExport's own
// "unknown id" behavior (no session registry is consulted for export,
// only hop.Session values).
func TestSessionExportEmptySessionIsEmptyHar(t *testing.T) {
	e := newTestEnv(t)
	resp, body := e.get(t, "/api/sessions/nope/export?format=har")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	var got trace.Har
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Log.Entries) != 0 {
		t.Errorf("entries = %d, want 0", len(got.Log.Entries))
	}
}

func TestTrafficHistoryLimitCapped(t *testing.T) {
	e := newTestEnv(t)
	var hops []trace.Hop
	for seq := uint64(1); seq <= 5; seq++ {
		hops = append(hops, trace.Hop{Seq: seq, To: "x", Status: 200})
	}
	writeHopsHistory(t, e, hops)

	got := getHistory(t, e, fmt.Sprintf("?limit=%d", 999999))
	if len(got.Hops) != 5 {
		t.Fatalf("got %d hops, want all 5 (absurd limit should cap, not error)", len(got.Hops))
	}
}
