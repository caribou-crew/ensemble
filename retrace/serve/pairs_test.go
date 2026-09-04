package serve

import (
	"net/http"
	"testing"
	"time"

	"github.com/caribou-crew/ensemble/retrace/diff"
	"github.com/caribou-crew/ensemble/retrace/pairs"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

// persistPair writes a pairing directly through pairs.Persist — the same
// call `retrace diff` makes — mirroring recordRun's own "build the fixture
// through production code" discipline (queue_test.go).
func persistPair(t *testing.T, cwd, appA, appB, flow, runB, verdict string) pairs.Pair {
	t.Helper()
	p, err := runs.PathsFor(runs.RunsRoot(cwd), appB, flow, runB)
	if err != nil {
		t.Fatalf("PathsFor: %v", err)
	}
	a := diff.RunRef{Kind: "bundle", Manifest: runs.Manifest{App: appA}}
	b := diff.RunRef{Kind: "run", RunID: runB, Manifest: runs.Manifest{App: appB}}
	s := diff.Summary{Schema: diff.SummarySchema, Flow: flow, Verdict: verdict, A: a, B: b}
	dir := pairs.DirFor(p.RunDir, a)
	pr, err := pairs.Persist(dir, s, time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("pairs.Persist: %v", err)
	}
	return pr
}

func TestGetPairsListsEveryPersistedCrossAppDiff(t *testing.T) {
	cwd := threeFlowProject(t)
	persistPair(t, cwd, "admin", "web", "login", runB, "changed")

	ts := newServer(t, cwd)
	body := mustOK(t, get(t, ts, "/api/pairs"), "GET /api/pairs")
	items, ok := body["pairs"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("pairs = %#v, want exactly one", body["pairs"])
	}
	row := items[0].(map[string]any)
	if row["appA"] != "admin" || row["appB"] != "web" {
		t.Errorf("row apps = %v/%v, want admin/web", row["appA"], row["appB"])
	}
}

func TestGetPairsIsNeverNull(t *testing.T) {
	cwd := threeFlowProject(t)
	ts := newServer(t, cwd)
	body := mustOK(t, get(t, ts, "/api/pairs"), "GET /api/pairs")
	if _, ok := body["pairs"].([]any); !ok {
		t.Fatalf("pairs = %#v, want an array (never null) even with nothing persisted", body["pairs"])
	}
}

func TestGetOnePairServesItsPersistedSummary(t *testing.T) {
	cwd := threeFlowProject(t)
	pr := persistPair(t, cwd, "admin", "web", "login", runB, "changed")

	ts := newServer(t, cwd)
	path := "/api/pairs/web/login/" + runB + "/" + pr.PairID
	body := mustOK(t, get(t, ts, path), "GET "+path)
	summary, ok := body["summary"].(map[string]any)
	if !ok {
		t.Fatalf("no summary in response: %#v", body)
	}
	if summary["verdict"] != "changed" {
		t.Errorf("verdict = %v, want changed", summary["verdict"])
	}
}

func TestGetAnUnpersistedPairIs404(t *testing.T) {
	cwd := threeFlowProject(t)
	ts := newServer(t, cwd)
	r := get(t, ts, "/api/pairs/web/login/"+runB+"/nothing__here")
	if r.status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", r.status)
	}
}
