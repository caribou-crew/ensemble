package serve

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caribou-crew/ensemble/core/trace"
)

// --- harness --------------------------------------------------------------

// sourcesServer builds an httptest server backed by NewWithSources, mirroring
// routes_test.go's own newServer helper for the single-root case.
func sourcesServer(t *testing.T, defaultDeps Deps, byRoot map[string]Deps, appRoot map[string]string) *httptest.Server {
	t.Helper()
	sources, err := NewSources(byRoot, appRoot)
	if err != nil {
		t.Fatalf("NewSources: %v", err)
	}
	ts := httptest.NewServer(NewWithSources(defaultDeps, &sources))
	t.Cleanup(ts.Close)
	return ts
}

// failingFlowProject records one flow ("checkout") for app that fails the
// same way threeFlowProject's web/cart does — side B answers 500 — so its
// ScoreOf matches web/cart's exactly, making cross-root tie-break
// (App, then Flow) the only thing that can decide their relative order.
func failingFlowProject(t *testing.T, app string) string {
	t.Helper()
	cwd := t.TempDir()
	recordRun(t, cwd, app, "checkout", runA, map[string][]byte{"checkout": shotPNG(t, white)},
		[]trace.Hop{hop(1, "GET", "/checkout", 200, `{"total":1}`)})
	acceptRef(t, cwd, app, "checkout", runA)
	recordRun(t, cwd, app, "checkout", runB, map[string][]byte{"checkout": shotPNG(t, white)},
		[]trace.Hop{hop(1, "GET", "/checkout", 500, `{"error":"boom"}`)})
	return cwd
}

// --- tests ------------------------------------------------------------

// TestSourcesBuildQueueWithOneRootMatchesSingleRootBuildQueue is the
// design.md/tasks.md compatibility claim: a Sources built from exactly one
// root must produce byte-identical output to calling BuildQueue on that
// root's Deps directly, since both now share sortItems.
func TestSourcesBuildQueueWithOneRootMatchesSingleRootBuildQueue(t *testing.T) {
	cwd := threeFlowProject(t)
	d := deps(t, cwd)

	want, err := BuildQueue(d)
	if err != nil {
		t.Fatalf("BuildQueue: %v", err)
	}

	sources, err := NewSources(map[string]Deps{cwd: d}, map[string]string{
		"web": cwd, "admin": cwd,
	})
	if err != nil {
		t.Fatalf("NewSources: %v", err)
	}
	got, err := sources.BuildQueue()
	if err != nil {
		t.Fatalf("Sources.BuildQueue: %v", err)
	}

	wantJSON, _ := json.Marshal(want)
	gotJSON, _ := json.Marshal(got)
	if string(wantJSON) != string(gotJSON) {
		t.Fatalf("Sources.BuildQueue over one root differs from single-root BuildQueue:\nwant %s\ngot  %s", wantJSON, gotJSON)
	}
}

// TestSourcesBuildQueueAggregatesAcrossRoots is design.md's running
// example, distilled: two roots, each with their own apps and runs, produce
// ONE worst-first list — including a cross-root tie-break, since
// mobile-app/checkout (root B) and web/cart (root A) are built to score
// identically.
func TestSourcesBuildQueueAggregatesAcrossRoots(t *testing.T) {
	rootA := threeFlowProject(t)
	rootB := failingFlowProject(t, "mobile-app")

	sources, err := NewSources(
		map[string]Deps{rootA: deps(t, rootA), rootB: deps(t, rootB)},
		map[string]string{
			"web": rootA, "admin": rootA,
			"mobile-app": rootB,
		},
	)
	if err != nil {
		t.Fatalf("NewSources: %v", err)
	}
	items, err := sources.BuildQueue()
	if err != nil {
		t.Fatalf("Sources.BuildQueue: %v", err)
	}

	got := queueOrder(items)
	want := []string{"mobile-app/checkout", "web/cart", "web/search", "admin/login", "web/login"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("aggregated queue order = %v, want %v\nitems: %+v", got, want, items)
	}
	if items[0].Score != items[1].Score {
		t.Fatalf("mobile-app/checkout and web/cart were built to score identically, got %v and %v", items[0].Score, items[1].Score)
	}
}

// TestServeWithSourcesQueueAggregatesOverHTTP is the HTTP-level counterpart:
// GET /api/queue on a server built with NewWithSources returns every app
// across every root, not just the default Deps' own.
func TestServeWithSourcesQueueAggregatesOverHTTP(t *testing.T) {
	rootA := threeFlowProject(t)
	rootB := failingFlowProject(t, "mobile-app")

	ts := sourcesServer(t, deps(t, rootA),
		map[string]Deps{rootA: deps(t, rootA), rootB: deps(t, rootB)},
		map[string]string{"web": rootA, "admin": rootA, "mobile-app": rootB},
	)
	items := queueFromREST(t, ts)
	got := queueOrder(items)
	want := []string{"mobile-app/checkout", "web/cart", "web/search", "admin/login", "web/login"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("GET /api/queue order = %v, want %v", got, want)
	}
}

// TestServeWithSourcesResolvesPerFlowRoutesToTheOwningRoot is
// retrace-serve-aggregation's own scenario: a detail route for an app
// mapped to a NON-default root must be computed against that root, not the
// server's default Deps/Cwd — proven here by an app that exists ONLY in
// rootB; if the route fell back to the default (rootA) it would 404.
func TestServeWithSourcesResolvesPerFlowRoutesToTheOwningRoot(t *testing.T) {
	rootA := threeFlowProject(t)
	rootB := failingFlowProject(t, "mobile-app")

	ts := sourcesServer(t, deps(t, rootA),
		map[string]Deps{rootA: deps(t, rootA), rootB: deps(t, rootB)},
		map[string]string{"web": rootA, "admin": rootA, "mobile-app": rootB},
	)
	status, verdict := verdictOf(t, ts, "mobile-app", "checkout")
	if status != 200 {
		t.Fatalf("GET /api/queue/mobile-app/checkout: status = %d, want 200 — the route did not resolve to rootB", status)
	}
	if verdict != "failed" {
		t.Fatalf("GET /api/queue/mobile-app/checkout: verdict = %q, want %q", verdict, "failed")
	}
}

// TestServeWithSourcesFallsBackToDefaultDepsForAnUnmappedApp matches
// DepsFor's own doc comment: an {app} absent from the repo config's map
// resolves to the server's default Deps, exactly as if there were no
// Sources at all.
func TestServeWithSourcesFallsBackToDefaultDepsForAnUnmappedApp(t *testing.T) {
	rootA := threeFlowProject(t)
	rootB := failingFlowProject(t, "mobile-app")

	ts := sourcesServer(t, deps(t, rootA),
		map[string]Deps{rootA: deps(t, rootA), rootB: deps(t, rootB)},
		map[string]string{"mobile-app": rootB}, // web/admin deliberately NOT in the map
	)
	status, verdict := verdictOf(t, ts, "web", "cart")
	if status != 200 {
		t.Fatalf("GET /api/queue/web/cart: status = %d, want 200 — an unmapped app should fall back to the default Deps", status)
	}
	if verdict != "failed" {
		t.Fatalf("GET /api/queue/web/cart: verdict = %q, want %q", verdict, "failed")
	}
}

// TestServeWithSourcesRuleWritesThroughTheOwningRoot is task 2.4: appending
// a wire rule for an app in a non-default root must write and reload THAT
// root's own overlay, not the server's default Deps.Cwd.
func TestServeWithSourcesRuleWritesThroughTheOwningRoot(t *testing.T) {
	rootA := threeFlowProject(t)
	rootB := failingFlowProject(t, "mobile-app")

	ts := sourcesServer(t, deps(t, rootA),
		map[string]Deps{rootA: deps(t, rootA), rootB: deps(t, rootB)},
		map[string]string{"web": rootA, "admin": rootA, "mobile-app": rootB},
	)

	r := post(t, ts, "/api/queue/mobile-app/checkout/rule",
		`{"field":"error","matcher":"exact","why":"test"}`)
	body := mustOK(t, r, "POST rule")
	if body["ok"] != true {
		t.Fatalf("POST rule: ok = %v, want true\n%s", body["ok"], r.body)
	}

	overlay := filepath.Join(rootB, ".retrace", "wire-rules.json")
	if _, err := os.Stat(overlay); err != nil {
		t.Fatalf("rule was not written to rootB's own overlay (%s): %v", overlay, err)
	}
	if _, err := os.Stat(filepath.Join(rootA, ".retrace", "wire-rules.json")); err == nil {
		t.Fatalf("rule for a rootB app was ALSO written to rootA's overlay — it must only land in the owning root")
	}
}
