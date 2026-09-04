package serve

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
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

// TestGetOnePairServesItsPersistedSummaryWhenBIsAReferenceBundle covers
// pairDirFor's refs.BundleDir branch (runB == runs.RefRunID) end-to-end
// through the actual HTTP route, not just at the pairs.List level — Task 1's
// TestListAlsoDiscoversAPairingPersistedUnderAReferenceBundle (retrace/pairs)
// proves List finds a bundle-rooted pairing, but nothing confirmed GET
// /api/pairs/{appB}/{flowB}/reference/{pairId} resolves one.
func TestGetOnePairServesItsPersistedSummaryWhenBIsAReferenceBundle(t *testing.T) {
	cwd := t.TempDir()
	appA, appB, flow := "admin", "web", "login"

	bundleDir := filepath.Join(runs.RefsRoot(cwd), appB, flow, runs.RefRunID)
	if err := os.MkdirAll(bundleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	a := diff.RunRef{Kind: "bundle", Manifest: runs.Manifest{App: appA}}
	b := diff.RunRef{Kind: "bundle", Manifest: runs.Manifest{App: appB}}
	s := diff.Summary{Schema: diff.SummarySchema, Flow: flow, Verdict: "changed", A: a, B: b}
	dir := pairs.DirFor(bundleDir, a)
	pr, err := pairs.Persist(dir, s, time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("pairs.Persist: %v", err)
	}

	ts := newServer(t, cwd)
	path := "/api/pairs/" + appB + "/" + flow + "/" + runs.RefRunID + "/" + pr.PairID
	body := mustOK(t, get(t, ts, path), "GET "+path)
	summary, ok := body["summary"].(map[string]any)
	if !ok {
		t.Fatalf("no summary in response: %#v", body)
	}
	if summary["verdict"] != "changed" {
		t.Errorf("verdict = %v, want changed", summary["verdict"])
	}
}

// crossAppPairFixture builds a real cross-app pairing whose A and B sides
// each have a real captured checkpoint shot on disk — A promoted into
// aRoot's committed reference bundle (mirroring the common "-a app@ref"
// shape), B a plain run directory under bRoot — then persists the pairing
// through pairs.Persist exactly like handlePairShot's fix (sideDirFor)
// expects to re-resolve it. It returns the persisted pair plus the raw PNG
// bytes each side was written with, so a test can assert GET
// .../shots/{side}/{name} serves back the EXACT same bytes, not just a 200
// — this is the coverage gap the final review found: nothing previously
// drove a/b through a real persisted pairing at the HTTP layer at all.
func crossAppPairFixture(t *testing.T, aRoot, aApp, bRoot, bApp, flow string) (pr pairs.Pair, aPNG, bPNG []byte) {
	t.Helper()
	aPNG = shotPNG(t, white)
	bPNG = shotPNG(t, blue)

	recordRun(t, aRoot, aApp, flow, runA, map[string][]byte{"shot": aPNG}, nil)
	acceptRef(t, aRoot, aApp, flow, runA)
	recordRun(t, bRoot, bApp, flow, runB, map[string][]byte{"shot": bPNG}, nil)

	a := diff.RunRef{Kind: "bundle", Manifest: runs.Manifest{
		App: aApp, Checkpoints: []runs.Checkpoint{{Name: "shot", File: "shots/shot.png", Width: 40, Height: 40}},
	}}
	b := diff.RunRef{Kind: "run", RunID: runB, Manifest: runs.Manifest{
		App: bApp, Checkpoints: []runs.Checkpoint{{Name: "shot", File: "shots/shot.png", Width: 40, Height: 40}},
	}}
	s := diff.Summary{Schema: diff.SummarySchema, Flow: flow, Verdict: "changed", A: a, B: b}

	bp, err := runs.PathsFor(runs.RunsRoot(bRoot), bApp, flow, runB)
	if err != nil {
		t.Fatalf("PathsFor: %v", err)
	}
	dir := pairs.DirFor(bp.RunDir, a)
	pr, err = pairs.Persist(dir, s, time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("pairs.Persist: %v", err)
	}
	return pr, aPNG, bPNG
}

// TestGetPairShotAResolvesThroughTheOwningRootAcrossASourcesConfig is the
// fix's central claim: 'a's directory is re-resolved through
// depsForApp(pr.AppA)+sideDirFor, not read verbatim off the persisted
// summary.json, so it finds A's shot in A's OWN root even when that root
// differs from the root serving B's request.
func TestGetPairShotAResolvesThroughTheOwningRootAcrossASourcesConfig(t *testing.T) {
	aRoot, bRoot := t.TempDir(), t.TempDir()
	pr, aPNG, _ := crossAppPairFixture(t, aRoot, "shop", bRoot, "web", "login")

	ts := sourcesServer(t, deps(t, bRoot),
		map[string]Deps{aRoot: deps(t, aRoot), bRoot: deps(t, bRoot)},
		map[string]string{"shop": aRoot, "web": bRoot},
	)
	path := "/api/pairs/web/login/" + runB + "/" + pr.PairID + "/shots/a/shot"
	r := get(t, ts, path)
	if r.status != http.StatusOK {
		t.Fatalf("GET %s: status = %d, want 200\n%s", path, r.status, r.body)
	}
	if r.ctype != "image/png" {
		t.Errorf("content-type = %q, want image/png", r.ctype)
	}
	if !bytes.Equal(r.body, aPNG) {
		t.Errorf("body did not match A's own fixture PNG — served the wrong side/root's bytes")
	}
}

// TestGetPairShotBStillWorksAfterTheAResolutionChange is regression
// coverage for the resolution-mechanism change: B's behavior is meant to be
// byte-for-byte unchanged, so it must still serve B's own bytes across the
// same multi-root config the A-side fix introduced.
func TestGetPairShotBStillWorksAfterTheAResolutionChange(t *testing.T) {
	aRoot, bRoot := t.TempDir(), t.TempDir()
	pr, _, bPNG := crossAppPairFixture(t, aRoot, "shop", bRoot, "web", "login")

	ts := sourcesServer(t, deps(t, bRoot),
		map[string]Deps{aRoot: deps(t, aRoot), bRoot: deps(t, bRoot)},
		map[string]string{"shop": aRoot, "web": bRoot},
	)
	path := "/api/pairs/web/login/" + runB + "/" + pr.PairID + "/shots/b/shot"
	r := get(t, ts, path)
	if r.status != http.StatusOK {
		t.Fatalf("GET %s: status = %d, want 200\n%s", path, r.status, r.body)
	}
	if !bytes.Equal(r.body, bPNG) {
		t.Errorf("body did not match B's own fixture PNG")
	}
}

// TestGetPairShotAWithNoSourcesConfigIsACleanNotFound is the single-root
// fallback case handlePairShot's fix doc comment calls out by name: without
// a Sources config mapping A's app to its own root, depsForApp("shop")
// falls back to the server's own default Deps (B's root) — so sideDirFor
// looks for A's bundle inside the WRONG tree and must fail with an honest
// 404, never a 500 and never (by construction, since it isn't even the
// right directory) B's own bytes.
func TestGetPairShotAWithNoSourcesConfigIsACleanNotFound(t *testing.T) {
	aRoot, bRoot := t.TempDir(), t.TempDir()
	pr, _, _ := crossAppPairFixture(t, aRoot, "shop", bRoot, "web", "login")

	ts := newServer(t, bRoot) // no Sources at all — single default Deps, rooted at bRoot
	path := "/api/pairs/web/login/" + runB + "/" + pr.PairID + "/shots/a/shot"
	r := get(t, ts, path)
	if r.status != http.StatusNotFound {
		t.Fatalf("GET %s: status = %d, want 404 (clean miss, not wrong data or a crash)\n%s", path, r.status, r.body)
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
