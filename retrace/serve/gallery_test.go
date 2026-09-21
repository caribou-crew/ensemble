package serve

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/caribou-crew/ensemble/retrace/diff"
	"github.com/caribou-crew/ensemble/retrace/suites"
)

const galleryInventory = `{"schema":"retrace/suites/1","suites":[{"id":"taxi","title":"Taxi","version":"v1","platforms":["web","ios","android"],"features":[{"id":"wallet","title":"Wallet","flows":[{"id":"wallet-home","title":"Wallet home","requiredPlanes":["functional","wire","visual"]},{"id":"atm","title":"Find ATM","requiredPlanes":["functional"]}]}]}]}`

func galleryPNG() []byte {
	return []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89\x00\x00\x00\rIDATx\x9cc\xf8\xff\xff?\x00\x05\xfe\x02\xfe\xa7\x9a\xa0\xa0\x00\x00\x00\x00IEND\xaeB`\x82")
}

func writeReport(t *testing.T, dir, name string, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func galleryAttempt(id, platform string, results []suites.Result) suites.Attempt {
	return suites.Attempt{Schema: suites.AttemptSchema, SuiteID: "taxi", SuiteVersion: "v1", AttemptID: id, Platform: platform,
		Git:        suites.Git{SHA: "0123456789abcdef0123456789abcdef01234567", Branch: "main"},
		BaselineID: "legacy", PolicyID: "p1", StartedAt: "2026-09-20T00:00:00Z", FinishedAt: "2026-09-20T00:01:00Z", Results: results}
}

// galleryProject builds: a web result linked to a saved pair (2 checkpoints,
// wire counts), an iOS result with an attached screen and a runner wire note, and
// an Android attempt that covers only one flow.
func galleryProject(t *testing.T) (Deps, string) {
	t.Helper()
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "retrace.suites.json"), []byte(galleryInventory), 0o600); err != nil {
		t.Fatal(err)
	}
	d := deps(t, cwd)
	runDir, err := sideDirFor(d, "taxi-candidate", "wallet-home", "run-1")
	if err != nil {
		t.Fatal(err)
	}
	pairDir := filepath.Join(runDir, "diffs", "pair-1")
	if err := os.MkdirAll(pairDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sum := diff.Summary{Schema: "retrace-diff/1", App: "taxi-candidate", Flow: "wallet-home",
		Checkpoints: []diff.CheckpointVerdict{{Name: "start", Verdict: "ok"}, {Name: "final", Verdict: "changed", DiffPct: 4.5}},
		Counts:      diff.Counts{Checkpoints: 2, PixelChanged: 1, WirePaired: 9, WireChanged: 2, WireMissing: 1, WireExtra: 3}}
	b, _ := json.Marshal(sum)
	if err := os.WriteFile(filepath.Join(pairDir, "summary.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "final.png"), galleryPNG(), 0o600); err != nil {
		t.Fatal(err)
	}
	web := writeReport(t, src, "web.json", galleryAttempt("web-1", "web", []suites.Result{
		{FlowID: "wallet-home", Planes: suites.Planes{Functional: "pass", Wire: "failed", Visual: "incomplete"},
			Evidence: &suites.Evidence{App: "taxi-candidate", Flow: "wallet-home", RunID: "run-1", PairID: "pair-1"}},
		{FlowID: "atm", Planes: suites.Planes{Functional: "pass", Wire: "not-applicable", Visual: "not-applicable"}},
	}))
	ios := writeReport(t, src, "ios.json", galleryAttempt("ios-1", "ios", []suites.Result{
		{FlowID: "atm", Planes: suites.Planes{Functional: "pass", Wire: "not-applicable", Visual: "not-applicable"},
			Screens: []suites.Screen{{Label: "Final screen", File: "final.png"}}, WireNote: "No reference wire exists for iOS."},
	}))
	for _, f := range []string{web, ios} {
		if _, err := suites.Import(cwd, f); err != nil {
			t.Fatal(err)
		}
	}
	return d, cwd
}

func buildID(t *testing.T, cwd string) string {
	t.Helper()
	items, err := suites.Load(cwd)
	if err != nil || len(items) != 1 || len(items[0].Builds) != 1 {
		t.Fatalf("load: %v %+v", err, items)
	}
	return items[0].Builds[0].ID
}

func TestGalleryTilesCarryPairsScreensAndExplicitWireState(t *testing.T) {
	d, cwd := galleryProject(t)
	h := New(d)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "http://localhost/api/suites/taxi/builds/"+buildID(t, cwd)+"/gallery", nil))
	if w.Code != 200 {
		t.Fatalf("gallery: %d %s", w.Code, w.Body)
	}
	var g Gallery
	if err := json.Unmarshal(w.Body.Bytes(), &g); err != nil {
		t.Fatal(err)
	}
	if len(g.Rows) != 2 || len(g.Platforms) != 3 {
		t.Fatalf("rows/platforms: %+v", g)
	}
	tile := func(row int, platform string) GalleryTile {
		for _, ti := range g.Rows[row].Tiles {
			if ti.Platform == platform {
				return ti
			}
		}
		t.Fatalf("no %s tile in row %d", platform, row)
		return GalleryTile{}
	}
	// Web wallet-home: a saved pair, final checkpoint chosen, wire counts shown.
	web := tile(0, "web")
	if web.Pair == nil || !web.Pair.Available || web.Pair.Checkpoint != "final" || web.Pair.Verdict != "changed" || len(web.Pair.Checkpoints) != 2 {
		t.Fatalf("web pair: %+v", web.Pair)
	}
	if !web.Wire.Represented || web.Wire.Counts == nil || *web.Wire.Counts != (WireCounts{Paired: 9, Changed: 2, Missing: 1, Extra: 3}) {
		t.Fatalf("web wire: %+v", web.Wire)
	}
	// iOS ATM: a stored screen, no pair; the runner's wire note is surfaced as-is.
	ios := tile(1, "ios")
	if len(ios.Screens) != 1 || ios.Screens[0].Label != "Final screen" || ios.AttemptID != "ios-1" {
		t.Fatalf("ios screens: %+v", ios)
	}
	if ios.Wire.Represented || ios.Wire.Note != "No reference wire exists for iOS." || ios.Wire.Counts != nil {
		t.Fatalf("ios wire: %+v", ios.Wire)
	}
	// A cell with no report is explicit, never blank or passing.
	and := tile(1, "android")
	if and.Status != "not-run" || and.AttemptID != "" || and.Wire.Represented || and.Wire.Note == "" {
		t.Fatalf("missing lane: %+v", and)
	}
	// Web result without a linked pair: not represented, with a default reason.
	webATM := tile(1, "web")
	if webATM.Pair != nil || webATM.Wire.Represented || webATM.Wire.Note == "" {
		t.Fatalf("unlinked web: %+v", webATM)
	}
}

func TestGalleryMissingPairIsReportedNotHidden(t *testing.T) {
	d, cwd := galleryProject(t)
	runDir, _ := sideDirFor(d, "taxi-candidate", "wallet-home", "run-1")
	if err := os.RemoveAll(filepath.Join(runDir, "diffs")); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	New(d).ServeHTTP(w, httptest.NewRequest("GET", "http://localhost/api/suites/taxi/builds/"+buildID(t, cwd)+"/gallery", nil))
	var g Gallery
	if err := json.Unmarshal(w.Body.Bytes(), &g); err != nil || w.Code != 200 {
		t.Fatalf("%d %v", w.Code, err)
	}
	var web GalleryTile
	for _, ti := range g.Rows[0].Tiles {
		if ti.Platform == "web" {
			web = ti
		}
	}
	if web.Pair == nil || web.Pair.Available || web.Pair.Error == "" || web.Wire.Represented || web.Wire.Counts != nil {
		t.Fatalf("a missing saved pair must be reported, not shown as compared: %+v", web)
	}
}

func TestGalleryRejectsUnknownSuiteAndBuild(t *testing.T) {
	d, cwd := galleryProject(t)
	for name, path := range map[string]string{
		"unknown suite": "/api/suites/nope/builds/" + buildID(t, cwd) + "/gallery",
		"unknown build": "/api/suites/taxi/builds/deadbeef/gallery",
	} {
		w := httptest.NewRecorder()
		New(d).ServeHTTP(w, httptest.NewRequest("GET", "http://localhost"+path, nil))
		if w.Code != 404 {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	w := httptest.NewRecorder()
	New(d).ServeHTTP(w, httptest.NewRequest("POST", "http://localhost/api/suites/taxi/builds/x/gallery", nil))
	if w.Code != 405 {
		t.Fatalf("POST: %d", w.Code)
	}
}

func TestSuiteScreenRouteServesImagesSafely(t *testing.T) {
	d, cwd := galleryProject(t)
	items, _ := suites.Load(cwd)
	var sha string
	for _, f := range items[0].Builds[0].Features[0].Flows {
		for _, c := range f.Platforms {
			if c.Platform == "ios" && c.Latest != nil && len(c.Latest.Screens) == 1 {
				sha = c.Latest.Screens[0].SHA256
			}
		}
	}
	if sha == "" {
		t.Fatal("no stored screen")
	}
	w := httptest.NewRecorder()
	New(d).ServeHTTP(w, httptest.NewRequest("GET", "http://localhost/api/suites/taxi/attempts/ios-1/screens/"+sha, nil))
	if w.Code != 200 || w.Header().Get("Content-Type") != "image/png" || !bytes.Equal(w.Body.Bytes(), galleryPNG()) {
		t.Fatalf("screen: %d %q", w.Code, w.Header().Get("Content-Type"))
	}
	if w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("missing nosniff")
	}
	for name, path := range map[string]string{
		"unreferenced":  "/api/suites/taxi/attempts/ios-1/screens/" + string(bytes.Repeat([]byte("0"), 64)),
		"not a hash":    "/api/suites/taxi/attempts/ios-1/screens/..%2f..%2fetc%2fpasswd",
		"other attempt": "/api/suites/taxi/attempts/web-1/screens/" + sha,
	} {
		w := httptest.NewRecorder()
		New(d).ServeHTTP(w, httptest.NewRequest("GET", "http://localhost"+path, nil))
		if w.Code == 200 {
			t.Fatalf("%s served", name)
		}
	}
}
