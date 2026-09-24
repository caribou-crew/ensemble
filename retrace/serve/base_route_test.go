package serve

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

// Reference = runA (white); runB and runC are both blue, so against the
// reference runC changed, but against base=runB it passes.
func baseFixture(t *testing.T) string {
	t.Helper()
	cwd := t.TempDir()
	h := []trace.Hop{hop(1, "GET", "/cart", 200, `{"n":1}`)}
	recordRun(t, cwd, "web", "cart", runA, map[string][]byte{"cart": shotPNG(t, white)}, h)
	acceptRef(t, cwd, "web", "cart", runA)
	recordRun(t, cwd, "web", "cart", runB, map[string][]byte{"cart": shotPNG(t, blue)}, h)
	recordRun(t, cwd, "web", "cart", runC, map[string][]byte{"cart": shotPNG(t, blue)}, h)
	return cwd
}

func runDir(t *testing.T, cwd, id string) string {
	t.Helper()
	p, err := runs.PathsFor(runs.RunsRoot(cwd), "web", "cart", id)
	if err != nil {
		t.Fatal(err)
	}
	return p.RunDir
}

func getSummary(t *testing.T, h http.Handler, path string) (int, map[string]any) {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "http://localhost"+path, nil))
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	return w.Code, body
}

func TestItemAtRunComparesAgainstBaseRun(t *testing.T) {
	cwd := baseFixture(t)
	h := New(deps(t, cwd))

	code, body := getSummary(t, h, "/api/queue/web/cart/runs/"+runC)
	if code != http.StatusOK {
		t.Fatalf("status %d: %v", code, body)
	}
	if v := body["summary"].(map[string]any)["verdict"]; v != "changed" {
		t.Fatalf("vs reference verdict = %v, want changed", v)
	}

	code, body = getSummary(t, h, "/api/queue/web/cart/runs/"+runC+"?base="+runB)
	if code != http.StatusOK {
		t.Fatalf("status %d: %v", code, body)
	}
	sum := body["summary"].(map[string]any)
	if v := sum["verdict"]; v != "pass" {
		t.Fatalf("vs base verdict = %v, want pass", v)
	}
	if a := sum["a"].(map[string]any)["runId"]; a != runB {
		t.Fatalf("side A = %v, want base %s", a, runB)
	}
}

func TestItemAtRunRefusesBaseEqualToRun(t *testing.T) {
	cwd := baseFixture(t)
	code, _ := getSummary(t, New(deps(t, cwd)), "/api/queue/web/cart/runs/"+runC+"?base="+runC)
	if code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", code)
	}
}

func TestShotAtRunServesBaseSide(t *testing.T) {
	cwd := baseFixture(t)
	w := httptest.NewRecorder()
	New(deps(t, cwd)).ServeHTTP(w, httptest.NewRequest("GET", "http://localhost/api/shots/web/cart/runs/"+runC+"/a/cart?base="+runB, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	want, err := os.ReadFile(filepath.Join(runDir(t, cwd, runB), "shots", "cart.png"))
	if err != nil {
		t.Fatal(err)
	}
	if w.Body.String() != string(want) {
		t.Fatalf("side A image is not the base run's shot")
	}
}

func TestEvidenceAtRunListsThatRunsVideos(t *testing.T) {
	cwd := baseFixture(t)
	recordRun(t, cwd, "web", "cart", "20260821T103000Z-ddddddd", map[string][]byte{"cart": shotPNG(t, blue)}, nil)
	dir := filepath.Join(runDir(t, cwd, runB), "videos")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "run.mp4"), []byte("mp4"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := New(deps(t, cwd))

	code, body := getSummary(t, h, "/api/evidence/web/cart/runs/"+runB)
	if code != http.StatusOK {
		t.Fatalf("status %d: %v", code, body)
	}
	if v := body["videos"].([]any); len(v) != 1 || v[0] != "run.mp4" {
		t.Fatalf("videos = %v, want [run.mp4]", v)
	}
	code, body = getSummary(t, h, "/api/evidence/web/cart")
	if v := body["videos"].([]any); code != http.StatusOK || len(v) != 0 {
		t.Fatalf("latest run should have no videos, got %d %v", code, v)
	}

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "http://localhost/api/videos/web/cart/runs/"+runB+"/run.mp4", nil))
	if w.Code != http.StatusOK || w.Body.String() != "mp4" {
		t.Fatalf("video at run: %d %q", w.Code, w.Body.String())
	}
}

func TestSurfacesListsRunsWithoutDiffing(t *testing.T) {
	cwd := baseFixture(t)
	code, body := getSummary(t, New(deps(t, cwd)), "/api/surfaces")
	if code != http.StatusOK {
		t.Fatalf("status %d: %v", code, body)
	}
	surfaces := body["surfaces"].([]any)
	if len(surfaces) != 1 {
		t.Fatalf("surfaces = %v, want one", surfaces)
	}
	runsList := surfaces[0].(map[string]any)["runs"].([]any)
	if len(runsList) != 3 || runsList[0].(map[string]any)["runId"] != runC {
		t.Fatalf("runs = %v, want 3 newest-first starting with %s", runsList, runC)
	}
}

func TestSummaryCacheMissesAfterOutOfBandAccept(t *testing.T) {
	cwd := baseFixture(t)
	d := deps(t, cwd)
	before, err := SummaryForRun(d, "web", "cart", runC)
	if err != nil || before.Verdict != "changed" {
		t.Fatalf("before accept: %v %v", before.Verdict, err)
	}
	acceptRef(t, cwd, "web", "cart", runB)
	after, err := SummaryForRun(d, "web", "cart", runC)
	if err != nil {
		t.Fatal(err)
	}
	if after.Verdict != "pass" {
		t.Fatalf("after accepting runB the cached verdict %q was served, want pass", after.Verdict)
	}
}
