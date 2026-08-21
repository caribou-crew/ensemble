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
	"strings"
	"testing"

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
