package serve

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/retrace/config"
	"github.com/caribou-crew/ensemble/retrace/diff"
	"github.com/caribou-crew/ensemble/retrace/refs"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

// --- fixtures -----------------------------------------------------------

var (
	white = color.RGBA{255, 255, 255, 255}
	blue  = color.RGBA{0, 0, 255, 255}
)

// shotPNG is a solid 40x40 image. Two different colours differ on every
// pixel, so a changed checkpoint is unambiguously changed rather than
// sitting near a threshold.
func shotPNG(t *testing.T, c color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 40, 40))
	for y := 0; y < 40; y++ {
		for x := 0; x < 40; x++ {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encoding a fixture shot: %v", err)
	}
	return buf.Bytes()
}

func hop(seq uint64, method, path string, status int, body string) trace.Hop {
	return trace.Hop{
		Schema: trace.SchemaVersion, Seq: seq, From: "web", To: "api",
		Method: method, Path: path, Status: status,
		T:    trace.Timings{Start: time.Date(2026, 8, 21, 10, 0, int(seq), 0, time.UTC), DoneMs: 10},
		Resp: trace.Payload{Headers: map[string]string{"content-type": "application/json"}, Body: body},
	}
}

// recordRun writes one run directory the way `retrace run` would leave it:
// a manifest naming its checkpoints, the shots themselves, and wire.jsonl.
func recordRun(t *testing.T, cwd, app, flow, runID string, shots map[string][]byte, hops []trace.Hop) runs.Paths {
	t.Helper()
	p, err := runs.Create(runs.RunsRoot(cwd), app, flow, runID)
	if err != nil {
		t.Fatalf("runs.Create(%s/%s/%s): %v", app, flow, runID, err)
	}
	m := runs.Manifest{
		App: app, Flow: flow, RunID: runID, Mode: runs.ModeStandalone,
		Git:        runs.Git{SHA: "deadbee", Branch: "main"},
		StartedAt:  time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC),
		FinishedAt: time.Date(2026, 8, 21, 10, 0, 5, 0, time.UTC),
		Capture:    runs.CaptureTrust{Status: trace.VerdictOK, Summary: "capture looks complete"},
		Wire:       runs.Counts{Calls: len(hops), Recorded: true},
	}
	for _, name := range sortedShotNames(shots) {
		rel := filepath.Join("shots", name+".png")
		if err := os.WriteFile(filepath.Join(p.RunDir, rel), shots[name], 0o644); err != nil {
			t.Fatalf("writing shot %s: %v", name, err)
		}
		m.Checkpoints = append(m.Checkpoints, runs.Checkpoint{Name: name, File: filepath.ToSlash(rel), Width: 40, Height: 40})
	}
	var wire bytes.Buffer
	for _, h := range hops {
		b, err := json.Marshal(h)
		if err != nil {
			t.Fatalf("marshalling a fixture hop: %v", err)
		}
		wire.Write(append(b, '\n'))
	}
	if err := os.WriteFile(p.WirePath, wire.Bytes(), 0o644); err != nil {
		t.Fatalf("writing wire.jsonl: %v", err)
	}
	if err := runs.WriteManifest(p, &m); err != nil {
		t.Fatalf("WriteManifest(%s): %v", runID, err)
	}
	return p
}

func sortedShotNames(shots map[string][]byte) []string {
	out := make([]string, 0, len(shots))
	for name := range shots {
		out = append(out, name)
	}
	// Deterministic checkpoint order in the manifest.
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// acceptRef promotes a run into the committed reference bundle, through
// refs.Accept — the same call the CLI and the REST verb make, so the
// fixture's baseline is built by production code rather than hand-copied.
func acceptRef(t *testing.T, cwd, app, flow, runID string) {
	t.Helper()
	if _, err := refs.Accept(refs.AcceptOptions{
		Cwd: cwd, RunsRoot: runs.RunsRoot(cwd), App: app, Flow: flow, RunID: runID,
	}); err != nil {
		t.Fatalf("refs.Accept(%s/%s/%s): %v", app, flow, runID, err)
	}
}

// deps builds the Deps a real server would have for cwd.
func deps(t *testing.T, cwd string) Deps {
	t.Helper()
	cfg, err := config.Discover(cwd)
	if err != nil {
		t.Fatalf("config.Discover: %v", err)
	}
	return Deps{Cwd: cwd, Cfg: cfg, Version: "test"}
}

const (
	runA = "20260821T100000Z-aaaaaaa"
	runB = "20260821T101000Z-bbbbbbb"
)

// threeFlowProject is the fixture the ordering tests share: one app with a
// failing, a changed and a passing flow, plus a SECOND app whose flow also
// passes, so the app tie-break is exercised and not merely assumed.
//
// The flow names are chosen so the expected order is neither the listing
// order (cart, login, search) nor its reverse: a sort that did nothing, and
// a sort that reversed, both fail.
func threeFlowProject(t *testing.T) string {
	t.Helper()
	cwd := t.TempDir()

	// web/cart — FAILED: side B answers 500, which no expected-status rule
	// excuses, so it is a gate.
	recordRun(t, cwd, "web", "cart", runA, map[string][]byte{"cart": shotPNG(t, white)},
		[]trace.Hop{hop(1, "GET", "/cart", 200, `{"total":1}`)})
	acceptRef(t, cwd, "web", "cart", runA)
	recordRun(t, cwd, "web", "cart", runB, map[string][]byte{"cart": shotPNG(t, white)},
		[]trace.Hop{hop(1, "GET", "/cart", 500, `{"error":"boom"}`)})

	// web/search — CHANGED: one checkpoint differs on every pixel.
	recordRun(t, cwd, "web", "search", runA, map[string][]byte{"results": shotPNG(t, white)},
		[]trace.Hop{hop(1, "GET", "/search", 200, `{"hits":1}`)})
	acceptRef(t, cwd, "web", "search", runA)
	recordRun(t, cwd, "web", "search", runB, map[string][]byte{"results": shotPNG(t, blue)},
		[]trace.Hop{hop(1, "GET", "/search", 200, `{"hits":1}`)})

	// web/login and admin/login — PASS: identical to their references.
	for _, app := range []string{"web", "admin"} {
		recordRun(t, cwd, app, "login", runA, map[string][]byte{"login": shotPNG(t, white)},
			[]trace.Hop{hop(1, "GET", "/login", 200, `{"ok":true}`)})
		acceptRef(t, cwd, app, "login", runA)
		recordRun(t, cwd, app, "login", runB, map[string][]byte{"login": shotPNG(t, white)},
			[]trace.Hop{hop(1, "GET", "/login", 200, `{"ok":true}`)})
	}
	return cwd
}

func queueOrder(items []Item) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.App + "/" + it.Flow
	}
	return out
}

// --- tests --------------------------------------------------------------

func TestQueueIsWorstFirstWithPassingFlowsLast(t *testing.T) {
	cwd := threeFlowProject(t)
	items, err := BuildQueue(deps(t, cwd))
	if err != nil {
		t.Fatalf("BuildQueue: %v", err)
	}
	got := queueOrder(items)
	want := []string{"web/cart", "web/search", "admin/login", "web/login"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("queue order = %v, want %v\nitems: %+v", got, want, items)
	}
	if items[0].Verdict != "failed" {
		t.Fatalf("worst item verdict = %q, want \"failed\"", items[0].Verdict)
	}
	if len(items[0].Gates) == 0 {
		t.Fatalf("the failing item carries no gates — the queue would show a red row with no reason")
	}
	// The two passing flows score exactly zero: that is what the UI
	// collapses on, so it is part of the contract and not an accident of
	// the formula.
	for _, it := range items[2:] {
		if it.Verdict != "pass" || it.Score != 0 {
			t.Fatalf("%s/%s: verdict %q score %v, want a passing flow at score 0", it.App, it.Flow, it.Verdict, it.Score)
		}
	}
	if !(items[0].Score > items[1].Score && items[1].Score > items[2].Score) {
		t.Fatalf("scores are not strictly worst-first: %v, %v, %v", items[0].Score, items[1].Score, items[2].Score)
	}
	// The run ids travel with the row: a reviewer must be able to say WHICH
	// run they are looking at and what it was compared against.
	if items[0].RunID != runB || items[0].RefRunID != runA {
		t.Fatalf("runId/refRunId = %q/%q, want %q/%q", items[0].RunID, items[0].RefRunID, runB, runA)
	}
	// Counts and Capture are the two fields the Zero-Value Global Constraint
	// names by name, and the score is computed from the Summary rather than
	// from the Item — so replacing either with its zero value leaves the row
	// sorted to the top, still saying "failed", while reporting that nothing
	// changed and that both captures are fine. Counts{} marshals to "wire
	// recorded, none seen" and CaptureBanner{} to two empty verdicts, and an
	// empty verdict ranks equal to "ok". Both are affirmatively reassuring,
	// which is the costume the third clause exists for.
	if items[0].Counts.UnexpectedStatuses != 1 {
		t.Fatalf("web/cart counts.unexpectedStatuses = %d, want 1 — the row says \"failed\" and reports that nothing was wrong: %+v", items[0].Counts.UnexpectedStatuses, items[0].Counts)
	}
	if items[1].Counts.PixelChanged != 1 {
		t.Fatalf("web/search counts.pixelChanged = %d, want 1 — the changed checkpoint did not reach the row: %+v", items[1].Counts.PixelChanged, items[1].Counts)
	}
	if items[0].Capture.A.Status != trace.VerdictOK || items[0].Capture.B.Status != trace.VerdictOK {
		t.Fatalf("web/cart capture = %+v, want both sides %q — an empty verdict ranks equal to ok, so an unassessed run would gate as clean", items[0].Capture, trace.VerdictOK)
	}
}

// informative reports whether a decoded JSON value carries anything at all:
// a non-empty string, a non-zero number, true, a non-empty array, or an
// object with at least one informative leaf.
func informative(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case string:
		return x != ""
	case float64:
		return x != 0
	case bool:
		return x
	case []any:
		return len(x) > 0
	case map[string]any:
		for _, e := range x {
			if informative(e) {
				return true
			}
		}
		return false
	}
	return true
}

// Every field the failing row carries must actually carry something.
//
// This walks the MARSHALLED item rather than listing the fields it checks,
// per global-constraints.md: a test that names its own inventory can only
// ever cover the fields its author remembered, and this one covers a field
// added to Item long after it was written, on the day it appears. Every
// key that is not omitempty is present precisely because it is supposed to
// mean something on every row — a key whose value is the type's zero is a
// key that reads as "measured, and fine".
func TestEveryFieldTheWorstRowCarriesIsPopulated(t *testing.T) {
	items, err := BuildQueue(deps(t, threeFlowProject(t)))
	if err != nil {
		t.Fatalf("BuildQueue: %v", err)
	}
	b, err := json.Marshal(items[0])
	if err != nil {
		t.Fatalf("marshalling the worst row: %v", err)
	}
	var row map[string]any
	if err := json.Unmarshal(b, &row); err != nil {
		t.Fatalf("the worst row is not a JSON object: %v\n%s", err, b)
	}
	// score is the one exception the walk cannot make: a passing row's
	// score is legitimately 0. items[0] is the FAILING row, so it is not.
	for key, val := range row {
		if !informative(val) {
			t.Fatalf("the failing row's %q is %v — every non-omitempty field on a queue row is there because it means something on every row, and a zero one reads as \"measured, and fine\"\n%s", key, val, b)
		}
	}
	// And the walk is measuring what it thinks it is: the fields that carry
	// data are all present, not omitted into vacuous success.
	for _, key := range []string{"app", "flow", "verdict", "score", "runId", "counts", "capture", "gates"} {
		if _, ok := row[key]; !ok {
			t.Fatalf("the failing row has no %q — the tags are the REST contract: %s", key, b)
		}
	}
}

// A flow whose reference cannot be resolved must be a ROW WITH A REASON.
// Dropping it would be indistinguishable from a flow that passed, and
// diffing the only run against itself (which is what refs.Resolve's
// newest-eligible-run fallback offers here) would report a confident
// "pass" computed by comparing a run with itself.
func TestAFlowWithNoReferenceAppearsWithAReasonNotSilentlyMissing(t *testing.T) {
	cwd := threeFlowProject(t)
	// A flow recorded once and never accepted: the ONLY thing "reference"
	// can resolve to is this same run.
	recordRun(t, cwd, "web", "onboarding", runA, map[string][]byte{"step1": shotPNG(t, white)},
		[]trace.Hop{hop(1, "GET", "/onboarding", 200, `{"step":1}`)})

	items, err := BuildQueue(deps(t, cwd))
	if err != nil {
		t.Fatalf("BuildQueue: %v", err)
	}
	var found *Item
	for i := range items {
		if items[i].App == "web" && items[i].Flow == "onboarding" {
			found = &items[i]
		}
	}
	if found == nil {
		t.Fatalf("web/onboarding is missing from the queue entirely: %v", queueOrder(items))
	}
	if found.Verdict == "pass" {
		t.Fatalf("web/onboarding reported %q — a flow with no accepted reference must never report a pass", found.Verdict)
	}
	if found.Score == 0 {
		t.Fatalf("web/onboarding scored 0, which is what the UI collapses — an unevaluable flow must sort with the worst")
	}
	joined := strings.Join(found.Gates, " ")
	if !strings.Contains(joined, "ref accept") {
		t.Fatalf("web/onboarding's gates do not say what to do about it: %q", joined)
	}
	// And it sorts above every flow that WAS evaluated and passed.
	if queueOrder(items)[len(items)-1] == "web/onboarding" {
		t.Fatalf("web/onboarding sorted last, below the passing flows: %v", queueOrder(items))
	}
}

// One broken flow must not take the whole queue down — it becomes an item
// whose Verdict is "quarantined" (a comparison that could not be made, which
// is what this row is) and whose Gates name the read error.
func TestQueueSurvivesAnUnreadableRunDirectory(t *testing.T) {
	cwd := threeFlowProject(t)

	// A flow with a good, accepted reference and a NEWER run whose manifest
	// cannot be read. "latest" resolves to the broken run, so the flow's
	// diff fails at the read.
	recordRun(t, cwd, "web", "profile", runA, map[string][]byte{"profile": shotPNG(t, white)},
		[]trace.Hop{hop(1, "GET", "/profile", 200, `{"id":1}`)})
	acceptRef(t, cwd, "web", "profile", runA)
	broken := recordRun(t, cwd, "web", "profile", runB, map[string][]byte{"profile": shotPNG(t, white)},
		[]trace.Hop{hop(1, "GET", "/profile", 200, `{"id":1}`)})
	if err := os.WriteFile(broken.ManifestPath, []byte("{not json"), 0o644); err != nil {
		t.Fatalf("corrupting the manifest: %v", err)
	}

	items, err := BuildQueue(deps(t, cwd))
	if err != nil {
		t.Fatalf("BuildQueue returned an error for ONE broken flow: %v", err)
	}
	// Every other flow is still there and still says what it said.
	got := queueOrder(items)
	for _, want := range []string{"web/cart", "web/search", "web/login", "admin/login", "web/profile"} {
		if !strings.Contains(strings.Join(got, ","), want) {
			t.Fatalf("%s vanished from the queue: %v", want, got)
		}
	}
	var broke Item
	for _, it := range items {
		if it.Flow == "profile" {
			broke = it
		}
	}
	// "quarantined", not "failed": nothing was compared, and diff.ExitCode
	// maps the two to different CI codes (3 vs 2). `retrace diff` on this
	// same flow exits 3, and two faces of one report must not disagree.
	if broke.Verdict != "quarantined" {
		t.Fatalf("the broken flow's verdict = %q, want \"quarantined\"", broke.Verdict)
	}
	joined := strings.Join(broke.Gates, " ")
	if !strings.Contains(joined, "manifest") {
		t.Fatalf("the broken flow's gates do not name the read error: %q", joined)
	}
}

// TestQueueSurvivesARunDirectoryTheProcessCannotRead is the same property
// against a real permission error rather than a corrupt file — the literal
// "unreadable directory", which takes a different path through
// runs.ReadManifest.
func TestQueueSurvivesARunDirectoryTheProcessCannotRead(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: mode bits do not deny anything")
	}
	cwd := threeFlowProject(t)
	recordRun(t, cwd, "web", "profile", runA, map[string][]byte{"profile": shotPNG(t, white)},
		[]trace.Hop{hop(1, "GET", "/profile", 200, `{"id":1}`)})
	acceptRef(t, cwd, "web", "profile", runA)
	broken := recordRun(t, cwd, "web", "profile", runB, map[string][]byte{"profile": shotPNG(t, white)},
		[]trace.Hop{hop(1, "GET", "/profile", 200, `{"id":1}`)})
	if err := os.Chmod(broken.RunDir, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	// Restored before t.TempDir's own cleanup runs (registered earlier, so
	// it runs later), which would otherwise fail to remove the tree.
	t.Cleanup(func() { _ = os.Chmod(broken.RunDir, 0o755) })

	items, err := BuildQueue(deps(t, cwd))
	if err != nil {
		t.Fatalf("BuildQueue returned an error for ONE unreadable run directory: %v", err)
	}
	var broke Item
	for _, it := range items {
		if it.Flow == "profile" {
			broke = it
		}
	}
	if broke.Verdict != "quarantined" || len(broke.Gates) == 0 {
		t.Fatalf("the unreadable flow = %+v, want verdict \"quarantined\" with a gate naming the error", broke)
	}
}

// patchedPNG is a solid image with a small square of a second colour in the
// top-left, so two flows that both "changed" do not produce the same diff
// mask. Solid-to-solid on both would give two byte-identical all-red diff
// images, which is value symmetry one level down: it would make the two
// flows' generated images indistinguishable even when they are correctly
// kept apart.
func patchedPNG(t *testing.T, base, patch color.RGBA, size int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 40, 40))
	for y := 0; y < 40; y++ {
		for x := 0; x < 40; x++ {
			if x < size && y < size {
				img.Set(x, y, patch)
			} else {
				img.Set(x, y, base)
			}
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encoding a fixture shot: %v", err)
	}
	return buf.Bytes()
}

// Checkpoint names are not globally unique — nothing stops web/login and
// admin/login both having a checkpoint called "login". diffDir keys the
// generated images on app AND flow for exactly that reason; drop either and
// the last flow diffed wins, and GET /api/shots/{app}/{flow}/diff/{name}
// serves another flow's image with a 200 and a valid PNG.
//
// The shared fixture masks this: the only two flows there that share a
// checkpoint name are the two that PASS, so neither writes a diff image at
// all. This one gives the name to two CHANGED flows, which is the fixture
// the property actually needs.
//
// It drives BuildQueue — the production writer, and what GET /api/queue
// runs — and then reads the two files, because a per-flow GET regenerates
// its own images on the way past and would look correct either way.
func TestTwoChangedFlowsSharingACheckpointNameDoNotShareADiffImage(t *testing.T) {
	cwd := t.TempDir()
	// Both apps have a flow "login" with a checkpoint "login", and both
	// changed — differently, so their diff masks are not the same picture.
	for _, c := range []struct {
		app string
		b   []byte
	}{
		{"web", shotPNG(t, blue)},                 // every pixel changed
		{"admin", patchedPNG(t, white, blue, 10)}, // only a 10x10 corner
	} {
		recordRun(t, cwd, c.app, "login", runA, map[string][]byte{"login": shotPNG(t, white)},
			[]trace.Hop{hop(1, "GET", "/login", 200, `{"ok":true}`)})
		acceptRef(t, cwd, c.app, "login", runA)
		recordRun(t, cwd, c.app, "login", runB, map[string][]byte{"login": c.b},
			[]trace.Hop{hop(1, "GET", "/login", 200, `{"ok":true}`)})
	}

	if _, err := BuildQueue(deps(t, cwd)); err != nil {
		t.Fatalf("BuildQueue: %v", err)
	}

	webDir, adminDir := diffDir(cwd, "web", "login"), diffDir(cwd, "admin", "login")
	if webDir == adminDir {
		t.Fatalf("web/login and admin/login share the diff directory %s — the last flow diffed wins and every later read serves the other flow's image", webDir)
	}
	// And each file is the picture its OWN flow's comparison produced: away
	// from the corner, web changed (red) and admin did not (grey).
	webPx := diskPixel(t, filepath.Join(webDir, "diff", "shots", "login.png"), 30, 30)
	adminPx := diskPixel(t, filepath.Join(adminDir, "diff", "shots", "login.png"), 30, 30)
	if want := ([4]uint32{0xffff, 0, 0, 0xffff}); webPx != want {
		t.Fatalf("web/login's diff at (30,30) is rgba%v, want red %v — every pixel of that checkpoint changed", webPx, want)
	}
	if adminPx == webPx {
		t.Fatalf("admin/login's diff at (30,30) is rgba%v, identical to web/login's — one flow's generated image was overwritten by the other's", adminPx)
	}
}

func diskPixel(t *testing.T, path string, x, y int) [4]uint32 {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("decoding %s: %v", path, err)
	}
	cr, cg, cb, ca := img.At(x, y).RGBA()
	return [4]uint32{cr, cg, cb, ca}
}

// The tie-break between two flows of the SAME app at the SAME score. Two
// rows that compare equal must appear in the same order on every reload, or
// a reviewer's queue reshuffles under them between two reads that found
// nothing new. Inverting `a.Flow < b.Flow` turns this red; the app-level
// tie-break is pinned by the shared fixture's admin/web pair.
func TestFlowsOfOneAppAtTheSameScoreAreOrderedByFlowName(t *testing.T) {
	cwd := t.TempDir()
	for _, flow := range []string{"alpha", "omega"} {
		recordRun(t, cwd, "web", flow, runA, map[string][]byte{"cp": shotPNG(t, white)},
			[]trace.Hop{hop(1, "GET", "/"+flow, 200, `{"ok":true}`)})
		acceptRef(t, cwd, "web", flow, runA)
		recordRun(t, cwd, "web", flow, runB, map[string][]byte{"cp": shotPNG(t, white)},
			[]trace.Hop{hop(1, "GET", "/"+flow, 200, `{"ok":true}`)})
	}
	items, err := BuildQueue(deps(t, cwd))
	if err != nil {
		t.Fatalf("BuildQueue: %v", err)
	}
	if len(items) != 2 || items[0].Score != items[1].Score {
		t.Fatalf("the fixture no longer produces two flows at one score: %+v", items)
	}
	if got := queueOrder(items); strings.Join(got, ",") != "web/alpha,web/omega" {
		t.Fatalf("queue order = %v, want [web/alpha web/omega] — two rows at one score must not reshuffle between reads", got)
	}
}

// ScoreOf is the ONE definition of the sort key and it is part of the REST
// contract, so each term is pinned separately: a weight that drifts (10 for
// a hop change instead of 100 for a gate) reorders every reviewer's queue.
func TestScoreOfWeighsEachPlaneAsTheRestContractSays(t *testing.T) {
	cases := []struct {
		name string
		s    diff.Summary
		want float64
	}{
		{"a passing flow scores zero", diff.Summary{Verdict: "pass"}, 0},
		{"a failed verdict", diff.Summary{Verdict: "failed"}, 1000},
		{"one gate", diff.Summary{Verdict: "changed", Gates: []string{"g"}}, 100},
		{"two gates", diff.Summary{Verdict: "changed", Gates: []string{"g", "h"}}, 200},
		{"a new hop route", diff.Summary{Verdict: "changed", Counts: diff.Counts{HopNew: 1}}, 10},
		{"a gone hop route", diff.Summary{Verdict: "changed", Counts: diff.Counts{HopGone: 1}}, 10},
		{"a changed checkpoint", diff.Summary{Verdict: "changed", Counts: diff.Counts{PixelChanged: 1}}, 1},
		{"a changed wire call", diff.Summary{Verdict: "changed", Counts: diff.Counts{WireChanged: 1}}, 1},
		{"a missing wire call", diff.Summary{Verdict: "changed", Counts: diff.Counts{WireMissing: 1}}, 1},
		{"an extra wire call", diff.Summary{Verdict: "changed", Counts: diff.Counts{WireExtra: 1}}, 1},
		{"the terms add up", diff.Summary{
			Verdict: "failed", Gates: []string{"g"},
			Counts: diff.Counts{HopNew: 1, HopGone: 1, PixelChanged: 2, WireChanged: 1, WireMissing: 1, WireExtra: 1},
		}, 1000 + 100 + 20 + 2 + 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ScoreOf(c.s); got != c.want {
				t.Fatalf("ScoreOf = %v, want %v", got, c.want)
			}
		})
	}
}

// A quarantined Summary is the zero-value trap this formula could most
// easily walk into: diff.Build returns BEFORE any plane is computed, so
// Counts is empty and Gates is empty, and a score keyed only on "failed"
// would give a run NOBODY COMPARED the same 0 as a run that was compared
// and matched — collapsed by the UI, invisible to the reviewer.
//
// Deleting "quarantined" from ScoreOf's switch turns this red.
func TestAQuarantinedFlowDoesNotScoreZeroLikeAPassingOne(t *testing.T) {
	quarantined := diff.Summary{
		Verdict:     "quarantined",
		Quarantined: []diff.Quarantine{{Side: "b", Reason: "the proxy was down for 40s"}},
	}
	if got := ScoreOf(quarantined); got <= ScoreOf(diff.Summary{Verdict: "pass"}) {
		t.Fatalf("ScoreOf(quarantined) = %v, which does not outrank a pass — a run that was never compared would be collapsed as clean", got)
	}
	// And the reason reaches the row, so a reviewer is not sent to read the
	// manifest to find out what "quarantined" meant.
	it := itemOf(quarantined)
	if len(it.Gates) == 0 || !strings.Contains(strings.Join(it.Gates, " "), "proxy was down") {
		t.Fatalf("the quarantine reason did not reach the item: %+v", it)
	}
}

// The Deps zero value must refuse, not default. An empty Cwd would root the
// runs tree at the PROCESS working directory, list nothing, and report an
// empty review queue — "nothing to review" is the most reassuring wrong
// answer this package can give. A nil Cfg would panic inside diff.Build.
func TestAZeroDepsIsRefusedRatherThanReportingAnEmptyQueue(t *testing.T) {
	cwd := threeFlowProject(t)
	full := deps(t, cwd)

	for _, c := range []struct {
		name string
		d    Deps
		want string
	}{
		{"no cwd", Deps{Cfg: full.Cfg}, "Cwd"},
		{"no config", Deps{Cwd: cwd}, "Cfg"},
		{"neither", Deps{}, "Cwd"},
	} {
		t.Run(c.name, func(t *testing.T) {
			items, err := BuildQueue(c.d)
			if err == nil {
				t.Fatalf("BuildQueue returned %d items and no error for %+v", len(items), c.d)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error does not name the missing field %q: %v", c.want, err)
			}
			if _, err := SummaryFor(c.d, "web", "cart"); err == nil {
				t.Fatalf("SummaryFor accepted a Deps with no %s", c.want)
			}
		})
	}
}

// The queue's app/flow values reach filepath.Join (diffDir) as well as the
// joins runs and refs each guard, so the guard is re-asserted at this seam.
func TestSummaryForRefusesAnAppOrFlowThatWouldEscapeTheRunsRoot(t *testing.T) {
	cwd := threeFlowProject(t)
	d := deps(t, cwd)
	for _, c := range [][2]string{{"..", "cart"}, {"web", ".."}, {"web", "../../etc"}, {"", "cart"}} {
		if _, err := SummaryFor(d, c[0], c[1]); err == nil {
			t.Fatalf("SummaryFor(%q, %q) was accepted", c[0], c[1])
		}
	}
}

// R-W. Item.Gates and Summary.Gates share the field name "gates" on the same
// REST surface, and they did NOT share a presence contract: every array field
// on Summary carries a bare json tag, while Item.Gates carried `omitempty`.
// So the queue row for a HEALTHY flow — the rows that are FINE — omitted the
// key entirely, and the first consumer of that row (the review UI's queue
// screen, which shows the gate count) reads `item.gates.length`, which throws
// synchronously on undefined. The blank first screen would have been caused
// by the passing flows.
//
// Asserted on the JSON BYTES, not on a decoded struct, because the struct
// cannot tell the three encodings apart: an absent key, `null` and `[]` all
// decode to a nil []string. Only one of the three is readable by a client
// without a special case, and `"gates":[]` is it — which also means dropping
// `omitempty` alone is not enough: itemGates must return an empty slice
// rather than the nil that `append([]string(nil))` produces for a flow with
// nothing to report.
func TestAPassingItemSerialisesGatesAsAnEmptyArray(t *testing.T) {
	items, err := BuildQueue(deps(t, threeFlowProject(t)))
	if err != nil {
		t.Fatalf("BuildQueue: %v", err)
	}
	var passing, failing *Item
	for i := range items {
		switch {
		case len(items[i].Gates) == 0 && passing == nil:
			passing = &items[i]
		case items[i].Verdict == "failed" && failing == nil:
			failing = &items[i]
		}
	}
	if passing == nil || failing == nil {
		t.Fatalf("fixture no longer has both a gate-free flow and a failing one: %v", queueOrder(items))
	}

	b, err := json.Marshal(passing)
	if err != nil {
		t.Fatalf("marshalling %s/%s: %v", passing.App, passing.Flow, err)
	}
	if !bytes.Contains(b, []byte(`"gates":[]`)) {
		t.Fatalf("the passing row %s/%s does not serialise \"gates\":[] — a client reading item.gates.length gets undefined and throws on the healthy case: %s",
			passing.App, passing.Flow, b)
	}

	// The mirror, and it is what stops "always emit []" from satisfying the
	// assertion above: a failing row's reasons still travel.
	fb, err := json.Marshal(failing)
	if err != nil {
		t.Fatalf("marshalling %s/%s: %v", failing.App, failing.Flow, err)
	}
	if bytes.Contains(fb, []byte(`"gates":[]`)) {
		t.Fatalf("the failing row %s/%s serialises an EMPTY gates array — a red row with no reason: %s", failing.App, failing.Flow, fb)
	}
}

// --- F7: a flow that changed must never reach the reviewer as "all clear" -

// reorderOnlyProject records a reference and then a candidate whose ONLY
// delta is the order of two otherwise-identical wire calls. Nothing is
// hand-built: the run directories are written the way `retrace run` leaves
// them, the reference is promoted through refs.Accept, and the verdict comes
// out of diff.Build.
//
// That combination lands on the three counts ScoreOf's weighted terms do not
// mention — Counts.WireMoved here, and Conformance / UnexpectedStatuses by
// the same argument — which diff.changed() DOES count.
func reorderOnlyProject(t *testing.T) string {
	t.Helper()
	cwd := t.TempDir()
	shots := map[string][]byte{"orders": shotPNG(t, white)}
	recordRun(t, cwd, "web", "orders", runA, shots, []trace.Hop{
		hop(1, "GET", "/orders", 200, `{"n":1}`),
		hop(2, "GET", "/profile", 200, `{"n":2}`),
	})
	acceptRef(t, cwd, "web", "orders", runA)
	// Same two calls, same bodies, same statuses — swapped.
	recordRun(t, cwd, "web", "orders", runB, shots, []trace.Hop{
		hop(1, "GET", "/profile", 200, `{"n":2}`),
		hop(2, "GET", "/orders", 200, `{"n":1}`),
	})
	return cwd
}

// The whole chain, in one test, because the defect only exists as a chain:
// ScoreOf omits WireMoved, so the row scores 0; EmptyReasonFor sees every
// item at 0 and answers "all-clear"; the UI renders "none of them needs
// attention" and files the changed row under a disclosure labelled
// "N passing". A flow that changed, reported as a clean project.
//
// Remove the floor from ScoreOf and every assertion below goes red.
func TestAReorderOnlyFlowIsNeverReportedAsAllClear(t *testing.T) {
	cwd := reorderOnlyProject(t)
	d := deps(t, cwd)

	sum, err := SummaryFor(d, "web", "orders")
	if err != nil {
		t.Fatalf("SummaryFor: %v", err)
	}
	// The fixture really is the shape this test claims: a "changed" verdict
	// whose only evidence is a count ScoreOf's terms never mention. Without
	// this, a fixture that drifted into (say) a changed checkpoint would keep
	// the test green while pinning nothing.
	if sum.Verdict != "changed" {
		t.Fatalf("the fixture is not a changed flow: verdict %q, counts %+v", sum.Verdict, sum.Counts)
	}
	if sum.Counts.WireMoved == 0 {
		t.Fatalf("the fixture recorded no reordering: counts %+v", sum.Counts)
	}
	if len(sum.Gates) != 0 || sum.Counts.PixelChanged != 0 || sum.Counts.WireChanged != 0 ||
		sum.Counts.WireMissing != 0 || sum.Counts.WireExtra != 0 ||
		sum.Counts.HopNew != 0 || sum.Counts.HopGone != 0 {
		t.Fatalf("the fixture carries evidence ScoreOf already weighs, so it cannot detect the gap: gates %v counts %+v", sum.Gates, sum.Counts)
	}

	if got := ScoreOf(sum); got <= 0 {
		t.Fatalf("ScoreOf(a changed flow) = %v — score 0 is the wire contract for \"nothing to act on\", and the UI collapses it under a disclosure labelled \"passing\"", got)
	}

	items, err := BuildQueue(d)
	if err != nil {
		t.Fatalf("BuildQueue: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected one flow in the queue, got %d: %v", len(items), queueOrder(items))
	}
	if items[0].Score <= 0 {
		t.Fatalf("the queue row for a changed flow scores %v", items[0].Score)
	}
	if got := EmptyReasonFor(items); got == EmptyAllClear {
		t.Fatalf("EmptyReasonFor said %q over a queue containing a flow that CHANGED — the reviewer is told every recorded flow was compared and none of them needs attention", got)
	}
}

// The other arm, and it is the one that keeps the floor honest: a genuinely
// passing flow must still score exactly zero, or the floor would have bought
// the partition by making every row need attention — and "all-clear" would
// become a state production can no longer construct.
func TestAPassingFlowStillScoresExactlyZeroAndEarnsAllClear(t *testing.T) {
	cwd := t.TempDir()
	shots := map[string][]byte{"login": shotPNG(t, white)}
	recordRun(t, cwd, "web", "login", runA, shots, []trace.Hop{hop(1, "GET", "/login", 200, `{"ok":true}`)})
	acceptRef(t, cwd, "web", "login", runA)
	recordRun(t, cwd, "web", "login", runB, shots, []trace.Hop{hop(1, "GET", "/login", 200, `{"ok":true}`)})

	items, err := BuildQueue(deps(t, cwd))
	if err != nil {
		t.Fatalf("BuildQueue: %v", err)
	}
	if len(items) != 1 || items[0].Verdict != "pass" {
		t.Fatalf("expected one passing flow, got %+v", items)
	}
	if items[0].Score != 0 {
		t.Fatalf("a passing flow scored %v, want 0 — the UI's collapse partition and EmptyReasonFor both key on exactly zero", items[0].Score)
	}
	if got := EmptyReasonFor(items); got != EmptyAllClear {
		t.Fatalf("EmptyReasonFor = %q over an all-passing queue, want %q", got, EmptyAllClear)
	}
}

// --- N-3: a row nobody could evaluate must not assert a capture verdict --

// captureOf pulls one side's capture object out of a row's MARSHALLED JSON.
//
// The bytes, not the struct, and that is the whole point of this test:
// unmarshalling into runs.CaptureTrust cannot tell `"status":""` from a
// `status` key that was never written, and it cannot tell either of them from
// a caller that forgot to set the field. TestAPassingItemSerialisesGatesAsAn
// EmptyArray avoids the same trap for the same reason.
func captureOf(t *testing.T, it Item, side string) map[string]any {
	t.Helper()
	b, err := json.Marshal(it)
	if err != nil {
		t.Fatalf("marshalling the row: %v", err)
	}
	var row map[string]any
	if err := json.Unmarshal(b, &row); err != nil {
		t.Fatalf("the row is not a JSON object: %v\n%s", err, b)
	}
	capture, ok := row["capture"].(map[string]any)
	if !ok {
		t.Fatalf("the row has no \"capture\" object: %s", b)
	}
	trust, ok := capture[side].(map[string]any)
	if !ok {
		t.Fatalf("the row's capture has no %q side: %s", side, b)
	}
	return trust
}

// A flow that could not be diffed at all carries a capture that SAYS it was
// never assessed, on both sides, as a value.
//
// The row is built by production: a flow recorded once and never accepted, so
// refs.Resolve has nothing to compare against and SummaryFor errors, which is
// the path brokenItem exists for. Nothing here hand-builds an Item.
//
// Delete the Capture field from brokenItem and every assertion below goes
// red, because `""` is what comes back.
func TestARowNobodyCouldEvaluateSaysItsCaptureWasNeverAssessed(t *testing.T) {
	cwd := t.TempDir()
	recordRun(t, cwd, "web", "onboarding", runA, map[string][]byte{"step1": shotPNG(t, white)},
		[]trace.Hop{hop(1, "GET", "/onboarding", 200, `{"step":1}`)})

	items, err := BuildQueue(deps(t, cwd))
	if err != nil {
		t.Fatalf("BuildQueue: %v", err)
	}
	if len(items) != 1 || items[0].Verdict != "quarantined" {
		t.Fatalf("expected one un-evaluable row, got %+v", items)
	}

	// BOTH sides. A fixture that checked only side a is symmetric in the
	// dimension under test — the two sides of a CaptureBanner are the most
	// interchangeable pair on this type.
	for _, side := range []string{"a", "b"} {
		trust := captureOf(t, items[0], side)
		status, _ := trust["status"].(string)
		if status == "" {
			t.Fatalf("capture.%s.status is the ZERO VALUE on a flow nobody compared — a consumer cannot tell it from a field somebody forgot to set, and the next consumer has no UI to paper over it: %v", side, trust)
		}
		if status == string(trace.VerdictOK) {
			t.Fatalf("capture.%s.status is %q on a flow nobody compared", side, status)
		}
		// The distinction the ruling asks for, machine-readable and without
		// a new trace.Verdict member: "assessed and found unusable" versus
		// "never assessed at all".
		reasons, _ := trust["reasons"].([]any)
		var codes []string
		for _, r := range reasons {
			if m, ok := r.(map[string]any); ok {
				if c, ok := m["code"].(string); ok {
					codes = append(codes, c)
				}
			}
		}
		if !slices.Contains(codes, "capture-not-assessed") {
			t.Fatalf("capture.%s carries no machine-readable \"never assessed\" reason — a consumer would have to parse prose, or know brokenItem exists: codes %v", side, codes)
		}
		summary, _ := trust["summary"].(string)
		if !strings.Contains(summary, "not assessed") {
			t.Fatalf("capture.%s.summary does not say it was never assessed: %q", side, summary)
		}
	}
}

// The contrast arm, and it is what keeps the arm above from being satisfied
// by "make every capture say not-assessed". A flow that WAS compared reports
// its real capture verdict, so "assessed and fine" and "never assessed" stay
// distinguishable in both directions.
func TestAFlowThatWasComparedStillReportsItsRealCaptureVerdict(t *testing.T) {
	cwd := t.TempDir()
	shots := map[string][]byte{"login": shotPNG(t, white)}
	recordRun(t, cwd, "web", "login", runA, shots, []trace.Hop{hop(1, "GET", "/login", 200, `{"ok":true}`)})
	acceptRef(t, cwd, "web", "login", runA)
	recordRun(t, cwd, "web", "login", runB, shots, []trace.Hop{hop(1, "GET", "/login", 200, `{"ok":true}`)})

	items, err := BuildQueue(deps(t, cwd))
	if err != nil {
		t.Fatalf("BuildQueue: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected one row, got %v", queueOrder(items))
	}
	for _, side := range []string{"a", "b"} {
		trust := captureOf(t, items[0], side)
		if got, _ := trust["status"].(string); got != string(trace.VerdictOK) {
			t.Fatalf("capture.%s.status = %q on a flow that was compared and passed, want %q", side, got, trace.VerdictOK)
		}
		if _, present := trust["reasons"]; present {
			t.Fatalf("capture.%s carries reasons on a clean capture: %v", side, trust)
		}
	}
}

// Every non-omitempty field on an UN-EVALUABLE row means something, walked
// over the marshalled bytes rather than a hand-written inventory — the same
// property TestEveryFieldTheWorstRowCarriesIsPopulated asserts for a row that
// WAS diffed. brokenItem is the other construction path onto this type, and
// it was the one where `capture` was vacuous.
//
// TWO DOCUMENTED EXCEPTIONS, and they are known open findings rather than
// properties this test is choosing not to hold. Both are bare tags, so the
// key is present carrying the type's zero — exactly as `capture` was:
//
//   - `runId` is "" — Item.RunID is Summary.B.RunID and brokenItem's Summary
//     has a zero B.
//   - `counts` is twelve zeros, which reads as "every plane was measured and
//     nothing changed" on a row where no plane was measured at all.
//     global-constraints.md names Counts{} by name as this trap.
//
// They are LISTED, not skipped silently, and the assertion below fails if
// either quietly starts passing — so the day they are fixed this block
// shrinks and the walk tightens, and any OTHER field that drifts into the
// same state still fails today. Reported and deliberately not fixed in the
// fix round 2 report: N-3's ruling names `capture`, and these are not it.
func TestEveryFieldAnUnEvaluableRowCarriesIsPopulated(t *testing.T) {
	cwd := t.TempDir()
	recordRun(t, cwd, "web", "onboarding", runA, map[string][]byte{"step1": shotPNG(t, white)},
		[]trace.Hop{hop(1, "GET", "/onboarding", 200, `{"step":1}`)})
	items, err := BuildQueue(deps(t, cwd))
	if err != nil {
		t.Fatalf("BuildQueue: %v", err)
	}
	b, err := json.Marshal(items[0])
	if err != nil {
		t.Fatalf("marshalling the row: %v", err)
	}
	var row map[string]any
	if err := json.Unmarshal(b, &row); err != nil {
		t.Fatalf("the row is not a JSON object: %v\n%s", err, b)
	}
	knownOpen := []string{"runId", "counts"}
	for key, val := range row {
		if slices.Contains(knownOpen, key) {
			continue
		}
		if !informative(val) {
			t.Fatalf("an un-evaluable row's %q is %v — a zero one reads as \"measured, and fine\"\n%s", key, val, b)
		}
	}
	// And the exceptions are real, not stale entries that have quietly
	// started passing. When one does, delete it from knownOpen.
	for _, key := range knownOpen {
		if informative(row[key]) {
			t.Fatalf("%q is now informative on an un-evaluable row — remove it from knownOpen and let the walk cover it", key)
		}
	}
}

// --- provenance -----------------------------------------------------------

func TestALocallyRecordedItemCarriesNoSource(t *testing.T) {
	cwd := t.TempDir()
	recordRun(t, cwd, "web", "login", runA, map[string][]byte{"login": shotPNG(t, white)},
		[]trace.Hop{hop(1, "GET", "/login", 200, `{"ok":true}`)})
	acceptRef(t, cwd, "web", "login", runA)
	recordRun(t, cwd, "web", "login", runB, map[string][]byte{"login": shotPNG(t, white)},
		[]trace.Hop{hop(1, "GET", "/login", 200, `{"ok":true}`)})

	items, err := BuildQueue(deps(t, cwd))
	if err != nil {
		t.Fatalf("BuildQueue: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	if items[0].Source != nil {
		t.Fatalf("Source = %+v, want nil for a locally recorded run", items[0].Source)
	}
	b, err := json.Marshal(items[0])
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	var row map[string]any
	if err := json.Unmarshal(b, &row); err != nil {
		t.Fatalf("unmarshalling: %v", err)
	}
	if _, ok := row["source"]; ok {
		t.Fatalf("a local item's JSON carries a \"source\" key at all: %s", b)
	}
}

func TestASyncedItemCarriesItsSource(t *testing.T) {
	cwd := t.TempDir()
	recordRun(t, cwd, "web", "login", runA, map[string][]byte{"login": shotPNG(t, white)},
		[]trace.Hop{hop(1, "GET", "/login", 200, `{"ok":true}`)})
	acceptRef(t, cwd, "web", "login", runA)
	p := recordRun(t, cwd, "web", "login", runB, map[string][]byte{"login": shotPNG(t, white)},
		[]trace.Hop{hop(1, "GET", "/login", 200, `{"ok":true}`)})

	syncedAt := time.Date(2026, 8, 27, 9, 0, 0, 0, time.UTC)
	if err := runs.WriteSource(p, runs.Source{
		Kind: runs.SourceKindCI, Workflow: "retrace-web", RunURL: "https://github.com/org/repo/actions/runs/1", SHA: "deadbee", SyncedAt: syncedAt,
	}); err != nil {
		t.Fatalf("runs.WriteSource: %v", err)
	}

	items, err := BuildQueue(deps(t, cwd))
	if err != nil {
		t.Fatalf("BuildQueue: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	got := items[0].Source
	if got == nil {
		t.Fatal("Source is nil for a synced item")
	}
	if got.Kind != runs.SourceKindCI || got.Workflow != "retrace-web" {
		t.Fatalf("Source = %+v, want kind %q workflow %q", got, runs.SourceKindCI, "retrace-web")
	}
	// This item does not affect the verdict: a synced run diffed against
	// the same reference an equivalent local run would use scores 0, same
	// as if source.json had never been written.
	if items[0].Score != 0 || items[0].Verdict != "pass" {
		t.Fatalf("a synced-but-identical run scored %v/%q, want 0/pass — provenance must never change a verdict", items[0].Score, items[0].Verdict)
	}
}

const runC = "20260821T102000Z-ccccccc"

// TestSummaryForRunComparesTheNamedRunNotLatest is SummaryForRun's primary
// contract: a reviewer picking an older run out of the CI candidate list
// must see THAT run's comparison, not silently get "latest" back, the way
// summaryFor's own default already does for SummaryFor.
func TestSummaryForRunComparesTheNamedRunNotLatest(t *testing.T) {
	cwd := t.TempDir()
	recordRun(t, cwd, "web", "login", runA, map[string][]byte{"login": shotPNG(t, white)},
		[]trace.Hop{hop(1, "GET", "/login", 200, `{"ok":true}`)})
	acceptRef(t, cwd, "web", "login", runA)
	recordRun(t, cwd, "web", "login", runB, map[string][]byte{"login": shotPNG(t, white)},
		[]trace.Hop{hop(1, "GET", "/login", 200, `{"ok":true}`)})
	recordRun(t, cwd, "web", "login", runC, map[string][]byte{"login": shotPNG(t, white)},
		[]trace.Hop{hop(1, "GET", "/login", 200, `{"ok":true}`)})

	d := deps(t, cwd)

	latest, err := SummaryFor(d, "web", "login")
	if err != nil {
		t.Fatalf("SummaryFor: %v", err)
	}
	if latest.B.RunID != runC {
		t.Fatalf("SummaryFor's B.RunID = %q, want latest run %q", latest.B.RunID, runC)
	}

	pinned, err := SummaryForRun(d, "web", "login", runB)
	if err != nil {
		t.Fatalf("SummaryForRun: %v", err)
	}
	if pinned.B.RunID != runB {
		t.Fatalf("SummaryForRun(%q)'s B.RunID = %q, want %q — the named run, not latest", runB, pinned.B.RunID, pinned.B.RunID)
	}
}

// TestSummaryForRunDoesNotShareItsDiffImageCacheWithLatest is the collision
// this feature exists to close: diff.writeCheckpointImages keys a
// generated PNG by checkpoint name alone, so a non-latest run's comparison
// must write under a run-scoped directory — never diffDir, which the
// "latest" queue reads and writes for the very same app/flow.
func TestSummaryForRunDoesNotShareItsDiffImageCacheWithLatest(t *testing.T) {
	cwd := t.TempDir()
	recordRun(t, cwd, "web", "login", runA, map[string][]byte{"login": shotPNG(t, white)},
		[]trace.Hop{hop(1, "GET", "/login", 200, `{"ok":true}`)})
	acceptRef(t, cwd, "web", "login", runA)
	// runB changed (blue); runC (latest) did not (white, identical to the
	// reference) — so the two comparisons' generated diff images, if they
	// landed in the same file, would visibly disagree about which one won.
	recordRun(t, cwd, "web", "login", runB, map[string][]byte{"login": shotPNG(t, blue)},
		[]trace.Hop{hop(1, "GET", "/login", 200, `{"ok":true}`)})
	recordRun(t, cwd, "web", "login", runC, map[string][]byte{"login": shotPNG(t, white)},
		[]trace.Hop{hop(1, "GET", "/login", 200, `{"ok":true}`)})

	d := deps(t, cwd)

	if _, err := SummaryFor(d, "web", "login"); err != nil {
		t.Fatalf("SummaryFor: %v", err)
	}
	pinned, err := SummaryForRun(d, "web", "login", runB)
	if err != nil {
		t.Fatalf("SummaryForRun: %v", err)
	}

	latestDir := diffDir(cwd, "web", "login")
	pinnedDir := diffDirForRun(cwd, "web", "login", runB)
	if latestDir == pinnedDir {
		t.Fatalf("diffDirForRun(%q) = %s, same as diffDir — a pinned run's cache must not alias the latest queue's own cache", runB, pinnedDir)
	}
	// runB actually changed against the reference, so it must have written
	// a diff image of its own, under its OWN directory.
	cp, found := checkpointNamed(pinned, "login")
	if !found || cp.Images.Diff == "" {
		t.Fatalf("SummaryForRun(%q)'s checkpoint has no diff image: %+v", runB, cp)
	}
	if _, err := os.Stat(filepath.Join(pinnedDir, "diff", "shots", "login.png")); err != nil {
		t.Fatalf("expected a diff image under the run-scoped cache: %v", err)
	}
	// And "latest" (runC, identical to the reference) must NOT have a diff
	// image sitting in ITS cache either — if runB's write had landed in
	// diffDir by mistake, this file would exist and be wrong (it would show
	// runB's change under runC's own unrelated, unchanged comparison).
	if _, err := os.Stat(filepath.Join(latestDir, "diff", "shots", "login.png")); err == nil {
		t.Fatalf("diffDir has a diff image for a checkpoint that did not change — runB's generated image leaked into the latest queue's cache")
	}
}
