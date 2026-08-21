package serve

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
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
// whose Verdict is "failed" and whose Gates name the read error.
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
	if broke.Verdict != "failed" {
		t.Fatalf("the broken flow's verdict = %q, want \"failed\"", broke.Verdict)
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
	if broke.Verdict != "failed" || len(broke.Gates) == 0 {
		t.Fatalf("the unreadable flow = %+v, want verdict \"failed\" with a gate naming the error", broke)
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
