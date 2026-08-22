package diff

// render_test.go covers RenderText, the DEFAULT human-facing view: every
// surface that is not --json goes through it. The Task 10 review found it
// at 44.7% coverage with its `BUDGET:` loop, its `GATE:` loop and its
// quarantine early-return all deletable green, and — the reason for the
// team lead's conformance ruling — no conformance section at all, which
// made Task 9's "unchecked" Kind invisible in exactly the view a human
// reads. A renderer whose sections can be deleted with a green suite is a
// renderer no test constrains.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/caribou-crew/ensemble/retrace/capture"
	"image/color"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/retrace/config"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

// renderedSummary is a Summary carrying one of everything RenderText can
// print, with every two-sided pair given DISTINCT values on each arm
// (capture a vs b, missing vs extra, new route vs gone route) so that
// swapping either arm for the other, or dropping one, changes the output.
// A fixture symmetric in those dimensions could not detect it.
func renderedSummary() Summary {
	return Summary{
		Schema: SummarySchema, App: "shop", Flow: "checkout",
		Verdict: "failed",
		Capture: CaptureBanner{
			A: runs.CaptureTrust{Status: trace.VerdictSuspect, Summary: "a quiet stretch on the reference"},
			B: runs.CaptureTrust{Status: trace.VerdictBroken, Summary: "the listener stopped on the candidate"},
		},
		Checkpoints: []CheckpointVerdict{
			{Name: "cart", Verdict: "ok", DiffPct: 0, DiffPctFine: 0},
			{Name: "receipt", Verdict: "changed", DiffPct: 2.14, DiffPctFine: 3.10,
				Images: CheckpointImages{Diff: "diff/shots/receipt.png"}},
			{Name: "gone", Verdict: "missing"},
			{Name: "fresh", Verdict: "added"},
			{Name: "broken", Verdict: "unreadable"},
		},
		Sections: []Section{{Name: "login", Entries: []Entry{
			{Method: "GET", NormalizedPath: "/session", Classes: []string{"changed"}},
		}}},
		Wire: Wire{
			Missing: []Call{{Method: "GET", Path: "/gone-call"}},
			Extra:   []Call{{Method: "POST", Path: "/extra-call"}},
		},
		Hops: HopDiff{
			NewRoutes:  []Route{{To: "pricing", Method: "GET", Path: "/price"}},
			GoneRoutes: []Route{{To: "legacy", Method: "POST", Path: "/old"}},
		},
		Conformance: []ConformanceFinding{
			{Method: "GET", Path: "/cart", Status: 200, Kind: "unchecked",
				Detail: "unresolvable $ref #/components/schemas/Missing"},
			{Method: "GET", Path: "/orders", Status: 500, Kind: "undocumented-status",
				Detail: "500 is not documented"},
		},
		Gates:   []string{"violation: GET /cart body[resp] total"},
		Budgets: []Gate{{Plane: "pixel", Threshold: 2, Observed: 3.5, Failed: true}, {Plane: "wire", Threshold: 5, Observed: 1.25}},
	}
}

func renderToString(s Summary) string {
	var b strings.Builder
	RenderText(&b, s)
	return b.String()
}

// TestRenderTextPrintsEveryReportSection pins each section of the report as
// a whole rendered line, not a loose substring. The review's only text
// assertions in the tree were strings.Contains(out, "suspect") and
// strings.Contains(out, "QUARANTINED"), under which the `GATE:` loop, the
// `BUDGET:` loop and the quarantine reasons were all deletable.
func TestRenderTextPrintsEveryReportSection(t *testing.T) {
	out := renderToString(renderedSummary())
	for _, want := range []string{
		// capture banner, both sides, each naming its own summary
		"capture a: suspect — a quiet stretch on the reference",
		"capture b: broken — the listener stopped on the candidate",
		// per-checkpoint lines, one per verdict shape
		"✓ cart     0.00%",
		"✗ receipt  2.14%  (fine 3.10%)  diff/shots/receipt.png",
		"✗ gone     missing",
		"✗ fresh    added",
		"✗ broken   unreadable",
		// wire sections and the unpaired calls on each side
		"-- login --",
		"  GET /session [changed]",
		"  MISSING GET /gone-call",
		"  EXTRA   POST /extra-call",
		// hop deltas, both directions
		"  NEW ROUTE   pricing GET /price",
		"  GONE ROUTE  legacy POST /old",
		// the gate reasons and every configured budget, failing or not —
		// a --no-fail reporting run must still show what it found
		"GATE: violation: GET /cart body[resp] total",
		"BUDGET: pixel 2.00% → 3.50% FAILED",
		"BUDGET: wire 5.00% → 1.25% ok",
		"VERDICT: failed",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("RenderText output is missing %q:\n%s", want, out)
		}
	}
}

// TestRenderTextPrintsAConformanceSectionWithUncheckedOnItsOwnLine pins the
// team lead's ruling. Task 9 added the fifth ConformanceFinding.Kind,
// "unchecked", so an unresolvable $ref / unparseable body / redaction-
// truncated body could never read as a verified pass. RenderText printed no
// conformance section at all, so in the default view that finding was
// invisible — the producer fixed and the consumer unwritten, which restores
// the silent pass one layer down.
func TestRenderTextPrintsAConformanceSectionWithUncheckedOnItsOwnLine(t *testing.T) {
	out := renderToString(renderedSummary())
	if !strings.Contains(out, "CONFORMANCE: 2 finding(s)") {
		t.Errorf("no conformance section header:\n%s", out)
	}
	if !strings.Contains(out, "1 unchecked") {
		t.Errorf("the conformance header does not call out the unchecked count:\n%s", out)
	}
	// Its own line, distinguishable from BOTH a pass (which prints nothing)
	// and a real violation (which prints its own Kind).
	if !strings.Contains(out, "UNCHECKED") {
		t.Errorf("the unchecked finding is not labelled UNCHECKED:\n%s", out)
	}
	if !strings.Contains(out, "/cart") || !strings.Contains(out, "unresolvable $ref") {
		t.Errorf("the unchecked finding does not name its call and reason:\n%s", out)
	}
	if !strings.Contains(out, "UNDOCUMENTED-STATUS") || !strings.Contains(out, "/orders") {
		t.Errorf("the real conformance finding is missing or not distinguishable from the unchecked one:\n%s", out)
	}
	var uncheckedLine, findingLine string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "/cart") && strings.Contains(line, "UNCHECKED") {
			uncheckedLine = line
		}
		if strings.Contains(line, "/orders") {
			findingLine = line
		}
	}
	if uncheckedLine == "" || findingLine == "" || uncheckedLine == findingLine {
		t.Fatalf("unchecked and violation must each get their own line; got %q / %q\n%s", uncheckedLine, findingLine, out)
	}
}

// TestRenderTextSaysNothingAboutConformanceWhenThereIsNothingToSay: the
// section is emitted only when findings exist. "CONFORMANCE: 0 findings"
// on a run with no OpenAPI spec configured would be the reassuring-value
// trap in a new costume — a clean-looking line derived from a check that
// never ran.
func TestRenderTextSaysNothingAboutConformanceWhenThereIsNothingToSay(t *testing.T) {
	s := renderedSummary()
	s.Conformance = nil
	if out := renderToString(s); strings.Contains(out, "CONFORMANCE") {
		t.Errorf("a Summary with no conformance findings must print no conformance section:\n%s", out)
	}
}

// TestRenderTextOnAQuarantinedSummaryPrintsTheReasonsAndNothingElse pins the
// early return: a quarantined comparison computed nothing, so printing
// checkpoint/gate/budget lines beside the banner would be reporting numbers
// nobody measured. The review found both the reasons and the return
// deletable green.
func TestRenderTextOnAQuarantinedSummaryPrintsTheReasonsAndNothingElse(t *testing.T) {
	s := renderedSummary()
	s.Verdict = "quarantined"
	s.Quarantined = []Quarantine{
		{Side: "a", Reason: "the reference recording is truncated"},
		{Side: "b", Reason: "the listener stopped on the candidate"},
	}
	out := renderToString(s)
	if !strings.Contains(out, "QUARANTINED") {
		t.Fatalf("no quarantine banner:\n%s", out)
	}
	// Both sides' reasons, each on its own line — a one-sided assertion
	// would leave the other arm deletable.
	if !strings.Contains(out, "  side a: the reference recording is truncated") {
		t.Errorf("side a's reason is missing:\n%s", out)
	}
	if !strings.Contains(out, "  side b: the listener stopped on the candidate") {
		t.Errorf("side b's reason is missing:\n%s", out)
	}
	for _, unwanted := range []string{"VERDICT:", "BUDGET:", "GATE:", "CONFORMANCE", "receipt", "NEW ROUTE", "MISSING"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("a quarantined report printed %q — nothing was compared, so there is nothing to report:\n%s", unwanted, out)
		}
	}
}

// TestRenderTextSurfacesAnUncheckedFindingOnAPassingRun is the ruling's
// actual scenario, driven through Build rather than a hand-built Summary:
// the spec's /cart 200 response is a $ref to a schema that does not exist,
// so the required-field check cannot run, the finding is "unchecked", and
// the verdict is legitimately "pass". The human reading that pass must
// still be told part of the response was never checked.
func TestRenderTextSurfacesAnUncheckedFindingOnAPassingRun(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	h := hop(1, "GET", "/cart", 200, "", `{"sku":"x"}`)
	writeWireFile(t, dirA, []trace.Hop{h})
	writeWireFile(t, dirB, []trace.Hop{h})

	specPath := writeSpecFile(t, `{
		"paths": {"/cart": {"get": {"responses": {"200": {"content": {"application/json": {
			"schema": {"$ref": "#/components/schemas/Missing"}
		}}}}}}},
		"components": {"schemas": {}}
	}`)
	aRef := RunRef{Kind: "run", Dir: dirA, Manifest: manifest("a", nil, nil, okCapture())}
	bRef := RunRef{Kind: "run", Dir: dirB, Manifest: manifest("b", nil, nil, okCapture())}
	cfg := baseConfig(t)
	cfg.Dir = ""
	cfg.OpenAPI = specPath

	s := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: aRef, B: bRef, Cfg: cfg})
	if s.Verdict != "pass" {
		t.Fatalf("test setup: verdict = %q, want pass", s.Verdict)
	}
	if s.Counts.Conformance != 1 {
		t.Fatalf("Counts.Conformance = %d, want 1 — the tally must carry the unchecked finding too", s.Counts.Conformance)
	}
	out := renderToString(s)
	if !strings.Contains(out, "VERDICT: pass") {
		t.Fatalf("test setup: no pass verdict in the report:\n%s", out)
	}
	if !strings.Contains(out, "UNCHECKED") || !strings.Contains(out, "/cart") {
		t.Fatalf("a pass beside a silent unchecked list — the human-facing report never mentions that /cart was not verified:\n%s", out)
	}
}

// TestRenderTextCountsOnlyUncheckedFindingsAsUnchecked guards the header's
// own arithmetic: a report that called every finding unchecked, or none of
// them, would still contain the word.
func TestRenderTextCountsOnlyUncheckedFindingsAsUnchecked(t *testing.T) {
	s := renderedSummary()
	s.Conformance = []ConformanceFinding{
		{Method: "GET", Path: "/a", Status: 200, Kind: "unknown-path", Detail: "no such path"},
		{Method: "GET", Path: "/b", Status: 200, Kind: "unknown-method", Detail: "no such method"},
	}
	out := renderToString(s)
	if !strings.Contains(out, "CONFORMANCE: 2 finding(s)\n") {
		t.Errorf("a findings list with nothing unchecked must not report an unchecked count:\n%s", out)
	}
	if strings.Contains(out, "unchecked") || strings.Contains(out, "UNCHECKED") {
		t.Errorf("no finding here is unchecked, yet the report says one is:\n%s", out)
	}
}

// --- ruling: an empty denominator emits NO gate ---------------------------
//
// observedFor divides for three of the four planes, and returning 0 when the
// denominator is empty reports a CLEAN gate for a plane that captured
// nothing. Perf already refused to emit one (TestAPerfBudgetOf0MsEmitsNoGate
// AtAll); wire and hop returned a reassuring 0 instead. All three are pinned
// the same way, and each is pinned in BOTH directions — no gate when the
// denominator is empty, a gate when it is not — because an assertion that
// only ever checks for absence passes just as happily against a budgetsOf
// that emits nothing at all.

func TestAWirePlaneThatPairedNothingEmitsNoGateAtAll(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	// Side A recorded a call; side B paired NOTHING with it. This is the
	// run with the least evidence in it, and under the old early return it
	// reported observed 0% against its wire budget: clean.
	writeWireFile(t, dirA, []trace.Hop{hop(1, "GET", "/cart", 200, "", `{"a":1}`)})
	writeWireFile(t, dirB, nil)

	aRef := RunRef{Kind: "run", Dir: dirA, Manifest: manifest("a", nil, nil, okCapture())}
	bRef := RunRef{Kind: "run", Dir: dirB, Manifest: manifest("b", nil, nil, okCapture())}
	cfg := baseConfig(t)
	cfg.Gates["wire"] = gatePct(2)

	s := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: aRef, B: bRef, Cfg: cfg})
	if s.Counts.WirePaired != 0 || s.Counts.WireMissing != 1 {
		t.Fatalf("test setup: Counts = %+v, want 0 paired and 1 missing", s.Counts)
	}
	for _, g := range s.Budgets {
		if g.Plane == "wire" {
			t.Fatalf("Budgets contains a wire entry (%+v) on a run that paired nothing — \"no data\" is not \"0%% changed\", and a clean gate derived from an absence of evidence is the zero-value trap", g)
		}
	}
}

func TestAWirePlaneThatPairedSomethingStillEmitsItsGate(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	h := hop(1, "GET", "/cart", 200, "", `{"a":1}`)
	writeWireFile(t, dirA, []trace.Hop{h})
	writeWireFile(t, dirB, []trace.Hop{h})

	aRef := RunRef{Kind: "run", Dir: dirA, Manifest: manifest("a", nil, nil, okCapture())}
	bRef := RunRef{Kind: "run", Dir: dirB, Manifest: manifest("b", nil, nil, okCapture())}
	cfg := baseConfig(t)
	cfg.Gates["wire"] = gatePct(2)

	s := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: aRef, B: bRef, Cfg: cfg})
	if gateFor(s, "wire") == nil {
		t.Fatalf("no wire Gate though one entry paired: %+v — suppressing an empty denominator must not suppress the plane", s.Budgets)
	}
}

func TestAHopPlaneWithNoServiceCountsEmitsNoGateAtAll(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	h := hop(1, "GET", "/cart", 200, "", `{}`)
	writeWireFile(t, dirA, []trace.Hop{h})
	writeWireFile(t, dirB, []trace.Hop{h})
	// No hops.jsonl on either side — a standalone run with no chain
	// captured at all, which is the common case, not an exotic one.

	aRef := RunRef{Kind: "run", Dir: dirA, Manifest: manifest("a", nil, nil, okCapture())}
	bRef := RunRef{Kind: "run", Dir: dirB, Manifest: manifest("b", nil, nil, okCapture())}
	cfg := baseConfig(t)
	cfg.Gates["hop"] = gatePct(10)

	s := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: aRef, B: bRef, Cfg: cfg})
	if len(s.Hops.ServiceCounts) != 0 {
		t.Fatalf("test setup: ServiceCounts = %+v, want empty", s.Hops.ServiceCounts)
	}
	for _, g := range s.Budgets {
		if g.Plane == "hop" {
			t.Fatalf("Budgets contains a hop entry (%+v) though no service counts exist — an unmeasurable plane must emit no Gate, exactly as perf already does", g)
		}
	}
}

func TestAHopPlaneWithServiceCountsStillEmitsItsGate(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	h := hop(1, "GET", "/cart", 200, "", `{}`)
	writeWireFile(t, dirA, []trace.Hop{h})
	writeWireFile(t, dirB, []trace.Hop{h})
	chain := []trace.Hop{{Seq: 1, TraceID: "t1", From: "client", To: "bff", Method: "GET", Path: "/cart", Status: 200}}
	writeChainFile(t, dirA, chain)
	writeChainFile(t, dirB, chain)

	aRef := RunRef{Kind: "run", Dir: dirA, Manifest: manifest("a", nil, nil, okCapture())}
	bRef := RunRef{Kind: "run", Dir: dirB, Manifest: manifest("b", nil, nil, okCapture())}
	cfg := baseConfig(t)
	cfg.Gates["hop"] = gatePct(10)

	s := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: aRef, B: bRef, Cfg: cfg})
	if gateFor(s, "hop") == nil {
		t.Fatalf("no hop Gate though ServiceCounts = %+v: %+v", s.Hops.ServiceCounts, s.Budgets)
	}
}

func TestAPerfPlaneWithABudgetStillEmitsItsGate(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	h := trace.Hop{Seq: 1, Method: "GET", Path: "/cart", Status: 200, T: trace.Timings{DoneMs: 50}}
	writeWireFile(t, dirA, []trace.Hop{h})
	writeWireFile(t, dirB, []trace.Hop{h})

	aRef := RunRef{Kind: "run", Dir: dirA, Manifest: manifest("a", nil, nil, okCapture())}
	bRef := RunRef{Kind: "run", Dir: dirB, Manifest: manifest("b", nil, nil, okCapture())}
	cfg := baseConfig(t)
	cfg.Flows = map[string]config.Flow{"flow": {PerfBudgetMs: 100}}
	cfg.Gates["perf"] = gatePct(10)

	s := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: aRef, B: bRef, Cfg: cfg})
	if gateFor(s, "perf") == nil {
		t.Fatalf("no perf Gate though BudgetMs = %v: %+v", s.Perf.BudgetMs, s.Budgets)
	}
}

// gateFor returns the Budgets entry for one plane, or nil.
func gateFor(s Summary, plane string) *Gate {
	for i := range s.Budgets {
		if s.Budgets[i].Plane == plane {
			return &s.Budgets[i]
		}
	}
	return nil
}

// TestAPixelPlaneWithNoCheckpointsEmitsNoGateAtAll is the fourth plane of
// the no-evidence-no-gate rule. Pixel is the one plane that does not divide
// — it takes a max over Checkpoints — but a max over nothing is 0, and 0
// here means "no pixels changed", not "nothing was compared".
//
// It also matters more than the other three: applyDefaults fills gates.pixel
// from thresholds.gate whenever the key is absent, so unlike wire and hop
// the pixel gate is emitted on essentially every run. A run that captured no
// screenshots reported "BUDGET: pixel 0.10% → 0.00% ok".
//
// This cannot mask a broken capture: capture/trust.go raises no-screenshots
// at VerdictDegraded when checkpoints were expected and none arrived, and
// quarantineCheck refuses that run before budgetsOf is ever reached. What is
// suppressed here is only a plane with no subject.
func TestAPixelPlaneWithNoCheckpointsEmitsNoGateAtAll(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	h := hop(1, "GET", "/cart", 200, "", `{}`)
	writeWireFile(t, dirA, []trace.Hop{h})
	writeWireFile(t, dirB, []trace.Hop{h})
	// No screenshots on either side: a flow whose adapter takes none, which
	// is an ordinary shape, not an exotic one.

	aRef := RunRef{Kind: "run", Dir: dirA, Manifest: manifest("a", nil, nil, okCapture())}
	bRef := RunRef{Kind: "run", Dir: dirB, Manifest: manifest("b", nil, nil, okCapture())}
	cfg := baseConfig(t)
	// The gate must be CONFIGURED, or this fixture passes whatever
	// observedFor does — budgetsOf emits nothing for an unmentioned plane
	// and the assertion below would hold with the guard deleted. In
	// production applyDefaults fills this key from thresholds.gate on every
	// run, so the configured case is the only one that ships.
	cfg.Gates["pixel"] = gatePct(0.1)

	s := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: aRef, B: bRef, Cfg: cfg})
	if len(s.Checkpoints) != 0 {
		t.Fatalf("test setup: Checkpoints = %+v, want none", s.Checkpoints)
	}
	if g := gateFor(s, "pixel"); g != nil {
		t.Fatalf("Budgets contains a pixel entry (%+v) though no checkpoint was compared — a max over zero checkpoints is an absence of evidence, not a clean screen", *g)
	}
}

func TestAPixelPlaneWithCheckpointsStillEmitsItsGate(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	h := hop(1, "GET", "/cart", 200, "", `{}`)
	writeWireFile(t, dirA, []trace.Hop{h})
	writeWireFile(t, dirB, []trace.Hop{h})
	base := color.RGBA{R: 10, G: 20, B: 30, A: 255}
	// 20x20 of 200x200 = 1% of the pixels.
	cpA := writeShot(t, dirA, "cart", solidPNG(t, 200, 200, base))
	cpB := writeShot(t, dirB, "cart", rectPNG(t, 200, 200, base, 0, 0, 20, 20, color.RGBA{R: 250, A: 255}))

	aRef := RunRef{Kind: "run", Dir: dirA, Manifest: manifest("a", []runs.Checkpoint{cpA}, nil, okCapture())}
	bRef := RunRef{Kind: "run", Dir: dirB, Manifest: manifest("b", []runs.Checkpoint{cpB}, nil, okCapture())}
	cfg := baseConfig(t)
	cfg.Gates["pixel"] = gatePct(0.1)

	s := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: aRef, B: bRef, Cfg: cfg})
	g := gateFor(s, "pixel")
	if g == nil {
		t.Fatalf("pixel Gate is missing; Budgets = %+v", s.Budgets)
	}
	if !closeTo(g.Observed, 1, 0.05) || !g.Failed {
		t.Fatalf("pixel Gate = %+v, want Observed ~1%% over a 0.1%% budget and Failed=true", *g)
	}
}

// TestAllowDegradedDropsThePixelGateAndKeepsTheNoScreenshotsReason pins the
// one case where "no evidence, no gate" and "checkpoints went missing"
// overlap, so that it is explicit rather than accidental.
//
// Normally the two never meet. capture/trust.go raises no-screenshots at
// VerdictDegraded when checkpoints were expected and none arrived, so
// quarantineCheck refuses that run and budgetsOf is never reached —
// suppressing the pixel gate can only ever drop a plane with no subject.
// Under --allow-degraded the degraded side DOES reach budgetsOf, and there
// the expected-but-missing run loses its pixel gate too.
//
// That is the intended trade. The operator opted into proceeding despite
// degradation, and the information is not lost: the no-screenshots reason
// still rides in the capture-trust banner, where "captured no screenshots"
// is a truer statement than a budget line reading "0.00% ok". What this
// test forbids is the third possibility — the gate reappearing as a pass.
//
// The trust value is produced by the real capture.Assess rather than
// hand-built, so the test fails if that upstream classification moves.
func TestAllowDegradedDropsThePixelGateAndKeepsTheNoScreenshotsReason(t *testing.T) {
	degraded := capture.Assess(capture.AssessInput{
		Hops:                []trace.Hop{hop(1, "GET", "/cart", 200, "", `{}`)},
		Checkpoints:         0,
		ExpectedCheckpoints: 3, // the last good run took three
		TestExitCode:        0,
		RequestsSeen:        1,
	})
	if degraded.Status != trace.VerdictDegraded {
		t.Fatalf("capture.Assess ranked this %q, want degraded — the premise of this test is that expected-but-missing screenshots are caught upstream", degraded.Status)
	}
	if !hasReason(degraded, "no-screenshots") {
		t.Fatalf("capture reasons = %+v, want a no-screenshots entry", degraded.Reasons)
	}

	dirA, dirB := t.TempDir(), t.TempDir()
	h := hop(1, "GET", "/cart", 200, "", `{}`)
	writeWireFile(t, dirA, []trace.Hop{h})
	writeWireFile(t, dirB, []trace.Hop{h})
	aRef := RunRef{Kind: "run", Dir: dirA, Manifest: manifest("a", nil, nil, okCapture())}
	bRef := RunRef{Kind: "run", Dir: dirB, Manifest: manifest("b", nil, nil, degraded)}
	cfg := baseConfig(t)
	cfg.Gates["pixel"] = gatePct(0.1)

	// Without the flag the run never reaches budgetsOf at all.
	q := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: aRef, B: bRef, Cfg: cfg})
	if q.Verdict != "quarantined" {
		t.Fatalf("verdict = %q, want quarantined — a degraded side is refused before any gate is computed", q.Verdict)
	}

	s := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: aRef, B: bRef, Cfg: cfg, AllowDegraded: true})
	if s.Verdict == "quarantined" {
		t.Fatalf("verdict = quarantined even under AllowDegraded")
	}
	if g := gateFor(s, "pixel"); g != nil {
		t.Fatalf("pixel Gate = %+v, want none — under --allow-degraded the missing screenshots cost this run its pixel gate, deliberately; what they must never do is come back as a passing budget", *g)
	}
	if !hasReason(s.Capture.B, "no-screenshots") {
		t.Fatalf("Capture.B reasons = %+v, want no-screenshots to survive into the banner — dropping the gate is only acceptable because this fact still reaches the report", s.Capture.B.Reasons)
	}
	// The human-facing banner carries the SUBSTANCE, not the code string:
	// "capture b: degraded — the test passed but captured no screenshots".
	// That prose is the whole justification for dropping the gate, so it is
	// asserted here rather than assumed.
	var buf bytes.Buffer
	RenderText(&buf, s)
	if !strings.Contains(buf.String(), "captured no screenshots") {
		t.Fatalf("RenderText does not say the screenshots were missing, so dropping the pixel gate would lose the fact entirely:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "capture b: degraded") {
		t.Fatalf("RenderText does not flag side B's capture as degraded:\n%s", buf.String())
	}
	// A MEASURED pixel line is what must never appear: "0.00% ok" for a
	// plane whose screenshots are missing is the reassuring number this
	// whole rule exists to withhold. The NOT EVALUATED line is the
	// opposite claim and is required — dropping the gate is only
	// acceptable while the report still says the gate was dropped.
	assertNoMeasuredBudgetLine(t, buf.String(), "pixel")
	if !strings.Contains(buf.String(), "BUDGET: pixel NOT EVALUATED") {
		t.Fatalf("RenderText drops the pixel gate silently — the plane is gated by this config and the reader is never told it went unmeasured:\n%s", buf.String())
	}
}

// assertNoMeasuredBudgetLine fails if RenderText printed a MEASURED budget
// row for one plane — a threshold, an observed percentage and ok/FAILED.
// The "not evaluated" row for the same plane is explicitly allowed: the two
// lines share a prefix and say opposite things, so a bare
// strings.Contains("BUDGET: pixel") cannot tell them apart, and the version
// of this helper that could not is what let an unevaluated gate read as a
// clean one.
func assertNoMeasuredBudgetLine(t *testing.T, out, plane string) {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "BUDGET: "+plane) && !strings.Contains(line, "NOT EVALUATED") {
			t.Fatalf("RenderText printed a measured budget row for %s, which this run has no evidence for: %q\n%s", plane, line, out)
		}
	}
}

func hasReason(c runs.CaptureTrust, code string) bool {
	for _, r := range c.Reasons {
		if r.Code == code {
			return true
		}
	}
	return false
}

// TestAnApiOnlyFlowReportsNoBudgetsAtAll records the visible behaviour
// change the four-plane rule creates, for Tasks 12, 13 and 16, which read
// Budgets.
//
// A flow that takes no screenshots and configures no other gate now reports
// ZERO budget entries. Before the rule it reported one — applyDefaults fills
// gates.pixel from thresholds.gate on every run, so the pixel entry was
// effectively always present, reading "0.00% ok" for a plane that was never
// checked. An absent plane is not a new shape for a consumer (a plane
// `gates:` never mentions has always been absent), but an EMPTY Budgets on a
// fully-defaulted config is newly reachable, and that is the part worth
// pinning.
//
// This asserts the semantic — no entries — and deliberately not the JSON
// encoding of the empty case, which is a separate open question.
//
// The empty Budgets is now accompanied by UnmeasuredGates naming pixel: an
// empty Budgets on a fully-defaulted config was newly reachable, and it
// read as "nothing gated here" on every surface. Zero MEASURED entries and
// "nothing gated" are different facts and both are asserted below.
func TestAnApiOnlyFlowReportsNoBudgetsAtAll(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	h := hop(1, "GET", "/cart", 200, "", `{}`)
	writeWireFile(t, dirA, []trace.Hop{h})
	writeWireFile(t, dirB, []trace.Hop{h})
	aRef := RunRef{Kind: "run", Dir: dirA, Manifest: manifest("a", nil, nil, okCapture())}
	bRef := RunRef{Kind: "run", Dir: dirB, Manifest: manifest("b", nil, nil, okCapture())}

	cfg := baseConfig(t)
	// Exactly what applyDefaults produces for a config that says nothing
	// about gates: a pixel budget and nothing else.
	cfg.Gates = map[string]config.Gate{"pixel": gatePct(0.1)}

	s := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: aRef, B: bRef, Cfg: cfg})
	if len(s.Budgets) != 0 {
		t.Fatalf("Budgets = %+v, want none — this flow took no screenshots, so its only configured plane has no subject", s.Budgets)
	}
	if s.Verdict != "pass" || ExitCode(s) != 0 {
		t.Fatalf("verdict = %q / exit %d, want pass / 0", s.Verdict, ExitCode(s))
	}
	// And the report must not print a MEASURED budget row for it. It does
	// print the honest counterpart: the plane IS gated by this config, and
	// "no BUDGET row for pixel" was previously read by every human as
	// "pixel is not gated here" — a claim about configuration the reader
	// cannot check from the report.
	var buf bytes.Buffer
	RenderText(&buf, s)
	assertNoMeasuredBudgetLine(t, buf.String(), "pixel")
	if !strings.Contains(buf.String(), "BUDGET: pixel NOT EVALUATED") {
		t.Fatalf("RenderText says nothing at all about the pixel plane this project gates:\n%s", buf.String())
	}
	if got := s.UnmeasuredGates; len(got) != 1 || got[0] != "pixel" {
		t.Fatalf("UnmeasuredGates = %v, want [pixel]", got)
	}
}

// TestEveryArrayFieldOnAnEmptySummaryMarshalsAsAnArray is the case
// TestSummaryJsonShapeIsStable's hand-built golden cannot reach: a Summary
// with nothing in it at all.
//
// null, absent and [] are three encodings of one meaning — "no entries" —
// and the nil arrives by too many routes to mean anything on its own.
// Carrying that ambiguity costs every consumer a null-guard, and the
// consumer who forgets does not misbehave quietly, it crashes:
// `summary.budgets.map(...)` throws on an API-only flow, which is an
// ordinary correct configuration. Tasks 12, 13, 15 and 16 are all unwritten,
// so the consumer who forgets is literally the one not written yet.
//
// This walks the marshalled JSON rather than listing field names, so a new
// array-valued field added later is covered the day it appears instead of
// the day someone remembers to extend a list.
func TestEveryArrayFieldOnAnEmptySummaryMarshalsAsAnArray(t *testing.T) {
	var s Summary
	s.ensureArrays()

	b, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var tree map[string]any
	if err := json.Unmarshal(b, &tree); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Exceptions, by exact path. Two kinds, and the difference matters:
	// the first is a distinction an empty array genuinely cannot carry, the
	// rest are fields this task is not allowed to touch.
	allowedNull := map[string]string{
		"wire.groups": "a nil *GroupNames means this flow has no group structure at all, " +
			"which is not the same as having groups that are empty — and it says so with a " +
			"pointer, the honest way to encode absent, rather than with the emptiness of an array",
	}

	var walk func(path string, v any)
	var nulls []string
	walk = func(path string, v any) {
		switch t := v.(type) {
		case map[string]any:
			for k, sub := range t {
				p := k
				if path != "" {
					p = path + "." + k
				}
				if sub == nil {
					if _, ok := allowedNull[p]; !ok {
						nulls = append(nulls, p)
					}
					continue
				}
				walk(p, sub)
			}
		case []any:
			for i, sub := range t {
				walk(fmt.Sprintf("%s[%d]", path, i), sub)
			}
		}
	}
	walk("", tree)

	if len(nulls) > 0 {
		sort.Strings(nulls)
		t.Fatalf("these fields marshalled as null on an empty Summary: %v\n"+
			"An array field must always encode as [] — a consumer that forgets the null-guard crashes rather than misbehaving quietly.\n"+
			"If one of these is a genuine \"not computed\" distinct from \"computed and empty\", it belongs in allowedNull with a comment saying why.\njson:\n%s",
			nulls, b)
	}
}

// TestBuildsOwnExitsAllProduceArrays checks the guarantee where consumers
// actually meet it. The empty-Summary test above pins ensureArrays; this
// pins that Build CALLS it, on every exit that returns a completed
// Summary — three of them: the ordinary exit and both quarantine exits
// (quarantineCheck's untrusted capture, incompleteCheck's truncated
// recording). The quarantine exits matter most, because they compute
// almost nothing and so are the likeliest to hand a consumer a nil —
// incompleteCheck's especially, since it is the exit this task added.
func TestBuildsOwnExitsAllProduceArrays(t *testing.T) {
	arraysOf := func(t *testing.T, s Summary) {
		t.Helper()
		b, err := json.Marshal(s)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var tree map[string]any
		if err := json.Unmarshal(b, &tree); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		for _, k := range []string{"checkpoints", "sections", "unexpectedStatuses", "gates", "budgets", "quarantined"} {
			if tree[k] == nil {
				t.Errorf("%q marshalled as null out of Build — every array field ships as []\njson: %s", k, b)
			}
		}
		w, _ := tree["wire"].(map[string]any)
		for _, k := range []string{"paired", "missing", "extra"} {
			if w == nil || w[k] == nil {
				t.Errorf("wire.%s marshalled as null out of Build", k)
			}
		}
		h, _ := tree["hops"].(map[string]any)
		for _, k := range []string{"serviceCounts", "newRoutes", "goneRoutes", "newErrors", "goneErrors", "requiredFailures"} {
			if h == nil || h[k] == nil {
				t.Errorf("hops.%s marshalled as null out of Build", k)
			}
		}
	}

	dirA, dirB := t.TempDir(), t.TempDir()
	hp := hop(1, "GET", "/cart", 200, "", `{}`)
	writeWireFile(t, dirA, []trace.Hop{hp})
	writeWireFile(t, dirB, []trace.Hop{hp})

	t.Run("the ordinary exit, on a flow with nothing to report", func(t *testing.T) {
		aRef := RunRef{Kind: "run", Dir: dirA, Manifest: manifest("a", nil, nil, okCapture())}
		bRef := RunRef{Kind: "run", Dir: dirB, Manifest: manifest("b", nil, nil, okCapture())}
		arraysOf(t, mustBuild(t, BuildInput{App: "a", Flow: "f", A: aRef, B: bRef, Cfg: baseConfig(t)}))
	})

	t.Run("the quarantine exit, which computes almost nothing", func(t *testing.T) {
		broken := runs.CaptureTrust{Status: trace.VerdictBroken, Summary: "the proxy never started"}
		aRef := RunRef{Kind: "run", Dir: dirA, Manifest: manifest("a", nil, nil, okCapture())}
		bRef := RunRef{Kind: "run", Dir: dirB, Manifest: manifest("b", nil, nil, broken)}
		s := mustBuild(t, BuildInput{App: "a", Flow: "f", A: aRef, B: bRef, Cfg: baseConfig(t)})
		if s.Verdict != "quarantined" {
			t.Fatalf("test setup: verdict = %q, want quarantined", s.Verdict)
		}
		arraysOf(t, s)
	})

	t.Run("the incompleteCheck exit, on a signal-killed recording", func(t *testing.T) {
		aRef := RunRef{Kind: "run", Dir: dirA, Manifest: manifest("a", nil, nil, okCapture())}
		aRef.Manifest.Test.ExitCode = -1
		bRef := RunRef{Kind: "run", Dir: dirB, Manifest: manifest("b", nil, nil, okCapture())}
		s := mustBuild(t, BuildInput{App: "a", Flow: "f", A: aRef, B: bRef, Cfg: baseConfig(t)})
		if s.Verdict != "quarantined" {
			t.Fatalf("test setup: verdict = %q, want quarantined", s.Verdict)
		}
		arraysOf(t, s)
	})
}

// TestAnEmptyConformancePlaneSaysWhetherItWasCheckedAtAll pins the fix for
// the one field in the array sweep where the empty value was genuinely
// ambiguous.
//
// conformance now flattens to [] like every other plane, so what an empty
// array MEANS has to be said rather than encoded in the difference between
// null and []. Without OpenAPIConfigured, "no spec configured" and "spec
// configured and every call conformed" are the same wire value, and
// never-checked reads as checked-and-clean — Task 9 added the "unchecked"
// finding kind to stop exactly that at finding scale, and this is the same
// failure at plane scale.
func TestAnEmptyConformancePlaneSaysWhetherItWasCheckedAtAll(t *testing.T) {
	clean := `{"paths": {"/cart": {"get": {"responses": {"200": {}}}}}}`

	t.Run("a spec configured and every call conforming", func(t *testing.T) {
		aRef, bRef := twoRuns(t,
			[]trace.Hop{hop(1, "GET", "/cart", 200, "", `{}`)},
			[]trace.Hop{hop(1, "GET", "/cart", 200, "", `{}`)}, nil, nil)
		cfg := baseConfig(t)
		cfg.Dir = ""
		cfg.OpenAPI = writeSpecFile(t, clean)

		s := mustBuild(t, BuildInput{App: "a", Flow: "f", A: aRef, B: bRef, Cfg: cfg})
		if len(s.Conformance) != 0 {
			t.Fatalf("test setup: Conformance = %+v, want empty", s.Conformance)
		}
		if !s.OpenAPIConfigured {
			t.Fatal("OpenAPIConfigured is false though a spec was configured — an empty conformance plane is then indistinguishable from one that was never checked")
		}
	})

	t.Run("no spec configured at all", func(t *testing.T) {
		aRef, bRef := twoRuns(t,
			[]trace.Hop{hop(1, "GET", "/cart", 200, "", `{}`)},
			[]trace.Hop{hop(1, "GET", "/cart", 200, "", `{}`)}, nil, nil)

		s := mustBuild(t, BuildInput{App: "a", Flow: "f", A: aRef, B: bRef, Cfg: baseConfig(t)})
		if s.OpenAPIConfigured {
			t.Fatal("OpenAPIConfigured is true though no spec was configured — this run's clean conformance plane would read as a verified pass")
		}
	})

	t.Run("both encode conformance as an array, so only the flag separates them", func(t *testing.T) {
		aRef, bRef := twoRuns(t,
			[]trace.Hop{hop(1, "GET", "/cart", 200, "", `{}`)},
			[]trace.Hop{hop(1, "GET", "/cart", 200, "", `{}`)}, nil, nil)
		cfg := baseConfig(t)
		cfg.Dir = ""
		cfg.OpenAPI = writeSpecFile(t, clean)

		for name, in := range map[string]BuildInput{
			"configured":     {App: "a", Flow: "f", A: aRef, B: bRef, Cfg: cfg},
			"not configured": {App: "a", Flow: "f", A: aRef, B: bRef, Cfg: baseConfig(t)},
		} {
			b, err := json.Marshal(mustBuild(t, in))
			if err != nil {
				t.Fatal(err)
			}
			var tree map[string]any
			if err := json.Unmarshal(b, &tree); err != nil {
				t.Fatal(err)
			}
			if tree["conformance"] == nil {
				t.Errorf("%s: conformance marshalled as null, want []", name)
			}
		}
	})
}

// TestAQuarantinedSummaryDoesNotClaimAnyPlaneWasChecked is the one path
// where OpenAPIConfigured being true does NOT imply the plane was checked:
// Build returns at the quarantine exit before any plane is computed.
//
// That is not a defect in the flag — the flag is a fact about
// configuration, and reporting false for a run that did configure a spec
// would be untrue. It is covered by the contract that already governs a
// quarantined Summary: every field is empty on purpose, and Verdict says so
// explicitly. This test exists so that contract is pinned rather than
// assumed, because it is the assumption the flag leans on.
func TestAQuarantinedSummaryDoesNotClaimAnyPlaneWasChecked(t *testing.T) {
	aRef, bRef := twoRuns(t,
		[]trace.Hop{hop(1, "GET", "/cart", 200, "", `{}`)},
		[]trace.Hop{hop(1, "GET", "/cart", 200, "", `{}`)}, nil, nil)
	bRef.Manifest.Capture = runs.CaptureTrust{Status: trace.VerdictBroken, Summary: "the proxy never started"}
	cfg := baseConfig(t)
	cfg.Dir = ""
	cfg.OpenAPI = writeSpecFile(t, `{"paths": {"/cart": {"get": {"responses": {"200": {}}}}}}`)

	s := mustBuild(t, BuildInput{App: "a", Flow: "f", A: aRef, B: bRef, Cfg: cfg})
	if s.Verdict != "quarantined" {
		t.Fatalf("test setup: verdict = %q, want quarantined", s.Verdict)
	}
	if len(s.Quarantined) == 0 {
		t.Fatal("a quarantined Summary must carry its reasons — that is what tells a consumer the empty planes are empty on purpose")
	}
	if len(s.Conformance) != 0 || len(s.Checkpoints) != 0 || len(s.Wire.Paired) != 0 {
		t.Fatalf("a quarantined Summary computed a plane: conformance=%d checkpoints=%d paired=%d",
			len(s.Conformance), len(s.Checkpoints), len(s.Wire.Paired))
	}
	// OpenAPIConfigured is set before either quarantine check runs — it is a
	// fact about configuration, not about what got computed — so it must
	// stay true here even though Conformance itself is empty. The rejected
	// alternative was setting the flag from inside the conformance block,
	// which this quarantine path never reaches; that alternative would
	// leave OpenAPIConfigured false here despite a spec being configured.
	if !s.OpenAPIConfigured {
		t.Fatal("OpenAPIConfigured is false on a quarantined run though a spec was configured — the flag must survive the quarantine exit unchanged")
	}
}

// TestAnUnchangedPairedCallShipsEveryArrayKeyThroughBuild is the wiring
// test for ensureEntryArrays. The contract test in wire_test.go calls that
// helper directly, which pins the helper and nothing else — three mutations
// survived it: dropping BodyDiff's initialisation, and dropping EITHER of
// the two loops that reach Entries at all.
//
// Entries live in two places on the Summary and both are populated from the
// same diff, so a fixture that checks one arm cannot see the other go
// missing. This asserts through Build, on the JSON, for both.
//
// Two subtests, not one, and this is load-bearing rather than decorative.
// BuildSections' no-groups path used to pass Wire.Paired straight through,
// so on an UNGROUPED run Sections[i].Entries[j] and Wire.Paired[k] were
// literally the same memory — dropping either loop in ensureArrays was
// invisible on that shape, because normalising one arm silently normalised
// the other through the shared backing array. Every bare-Entry fixture in
// this suite went through twoRuns, which always builds ungrouped, so that
// blind spot went unmeasured. buildSection now copies unconditionally (see
// order.go), which removes the aliasing on both paths, but the grouped
// case is kept anyway: it pins the behaviour Build promises rather than
// BuildSections' current implementation of it.
func TestAnUnchangedPairedCallShipsEveryArrayKeyThroughBuild(t *testing.T) {
	keys := []string{"classes", "bodyDiff", "bodyTolerated", "bodyViolations", "bodyIgnored", "orderingChanges", "headerDiff"}
	check := func(t *testing.T, where string, raw any) {
		t.Helper()
		e, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("%s is not an object: %v", where, raw)
		}
		for _, k := range keys {
			v, present := e[k]
			if !present {
				t.Errorf("%s.%s is ABSENT — an unchanged paired call must still carry every array key, or entry.%s.map(...) throws on the commonest row in the report", where, k, k)
				continue
			}
			if v == nil {
				t.Errorf("%s.%s marshalled as null, want []", where, k)
			}
		}
	}

	checkBuild := func(t *testing.T, s Summary) {
		t.Helper()
		if len(s.Wire.Paired) == 0 {
			t.Fatalf("test setup: nothing paired")
		}
		if len(s.Sections) == 0 || len(s.Sections[0].Entries) == 0 {
			t.Fatalf("test setup: no section entries; Sections = %+v", s.Sections)
		}

		b, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		var tree map[string]any
		if err := json.Unmarshal(b, &tree); err != nil {
			t.Fatal(err)
		}

		wire, _ := tree["wire"].(map[string]any)
		paired, _ := wire["paired"].([]any)
		if len(paired) == 0 {
			t.Fatalf("wire.paired is empty in the JSON")
		}
		check(t, "wire.paired[0]", paired[0])

		sections, _ := tree["sections"].([]any)
		if len(sections) == 0 {
			t.Fatalf("sections is empty in the JSON")
		}
		sec0, _ := sections[0].(map[string]any)
		entries, _ := sec0["entries"].([]any)
		if len(entries) == 0 {
			t.Fatalf("sections[0].entries is empty in the JSON")
		}
		check(t, "sections[0].entries[0]", entries[0])
	}

	// Identical on both sides: the unchanged paired call, the most common
	// row in any summary and the one where all seven arrays are empty.
	h := hop(1, "GET", "/cart", 200, "", `{"ok":true}`)

	t.Run("ungrouped", func(t *testing.T) {
		aRef, bRef := twoRuns(t, []trace.Hop{h}, []trace.Hop{h}, nil, nil)
		s := mustBuild(t, BuildInput{App: "a", Flow: "f", A: aRef, B: bRef, Cfg: baseConfig(t)})
		checkBuild(t, s)
	})

	t.Run("grouped", func(t *testing.T) {
		dirA, dirB := t.TempDir(), t.TempDir()
		hg := hop(1, "GET", "/cart", 200, "", `{"ok":true}`)
		hg.T = trace.Timings{Start: time.Date(2024, 1, 1, 0, 0, 5, 0, time.UTC)}
		writeWireFile(t, dirA, []trace.Hop{hg})
		writeWireFile(t, dirB, []trace.Hop{hg})

		groups := []runs.Group{
			{Name: "checkout", StartedAt: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
				EndedAt: time.Date(2024, 1, 1, 0, 0, 10, 0, time.UTC)},
		}
		aRef := RunRef{Kind: "run", Dir: dirA, Manifest: manifest("a", nil, groups, okCapture())}
		bRef := RunRef{Kind: "run", Dir: dirB, Manifest: manifest("b", nil, groups, okCapture())}
		cfg := baseConfig(t)
		opts, err := OptionsFor(cfg, aRef.Manifest, bRef.Manifest)
		if err != nil {
			t.Fatalf("OptionsFor: %v", err)
		}

		s := mustBuild(t, BuildInput{App: "a", Flow: "f", A: aRef, B: bRef, Cfg: cfg, Options: opts})
		if len(s.Sections) != 1 || s.Sections[0].Name != "checkout" {
			t.Fatalf("test setup: Sections = %+v, want one section named checkout", s.Sections)
		}
		checkBuild(t, s)
	})
}

// TestRenderTextNamesAGateItCouldNotEvaluate pins the TEXT face of the
// unmeasured-gate signal — the face a human reads in CI logs, and the one
// that printed `VERDICT: pass` and no other word about a gate the project
// had configured.
//
// It asserts the plane name AND the sentence, because the plane name alone
// would also be produced by an ordinary `BUDGET: perf …` row: a reader must
// be able to tell "measured, and within budget" from "never measured", and
// those two are one word apart in this output.
func TestRenderTextNamesAGateItCouldNotEvaluate(t *testing.T) {
	var buf bytes.Buffer
	RenderText(&buf, Summary{
		Verdict:         "failed",
		Capture:         CaptureBanner{A: okCapture(), B: okCapture()},
		Budgets:         []Gate{},
		UnmeasuredGates: []string{"perf", "pixel"},
		Gates:           []string{"gate not evaluated: perf is gated and named in fail_on, but this run carried no evidence to measure it against — that is not a gate that passed"},
	})
	out := buf.String()
	for _, plane := range []string{"perf", "pixel"} {
		if !strings.Contains(out, plane+" NOT EVALUATED") {
			t.Errorf("text report does not name %s as unevaluated:\n%s", plane, out)
		}
	}
	if !strings.Contains(out, "not a gate that passed") {
		t.Errorf("text report names the planes but never says what that means:\n%s", out)
	}
}

// TestRenderTextSaysNothingAboutUnevaluatedGatesWhenEveryGateRan is the
// other arm: the line must not appear on a run whose gates all measured, or
// it becomes noise every reader learns to skip — which is the same as not
// printing it.
func TestRenderTextSaysNothingAboutUnevaluatedGatesWhenEveryGateRan(t *testing.T) {
	var buf bytes.Buffer
	RenderText(&buf, Summary{
		Verdict:         "pass",
		Capture:         CaptureBanner{A: okCapture(), B: okCapture()},
		Budgets:         []Gate{{Plane: "perf", Threshold: 10, Observed: 1}},
		UnmeasuredGates: []string{},
	})
	if out := buf.String(); strings.Contains(out, "NOT EVALUATED") {
		t.Errorf("text report claims a gate was not evaluated on a run where every gate ran:\n%s", out)
	}
}
