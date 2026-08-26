package diff

import (
	"bytes"
	"image/color"
	"strings"
	"testing"

	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/retrace/config"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

// --- fixtures -------------------------------------------------------------

// summaryWithSignals builds a Summary whose UNDERLYING fields produce sig.
// It deliberately does not set TriageSignals anywhere: each bit is driven
// through the real field signalsOf reads, so every test built on this helper
// exercises the derivation as well as the table. TestTheFixtureProducesThe
// SignalsItClaims pins that round trip — without it a fixture that quietly
// stopped setting a field would make every table assertion below pass by
// classifying the wrong vector.
func summaryWithSignals(sig TriageSignals, verdict string) Summary {
	s := Summary{
		Verdict: verdict,
		Capture: CaptureBanner{A: okCapture(), B: okCapture()},
	}
	if sig.Pixel {
		s.Checkpoints = []CheckpointVerdict{{Name: "cart", Verdict: "changed", DiffPct: 4}}
	}
	if sig.Wire {
		// An EXTRA call: unambiguously the client's decision, and one of the
		// clauses that needs no scope to attribute. Driving the fixture
		// through Counts.WireChanged instead would exercise only the
		// unattributable backstop.
		s.Counts.WireExtra = 1
	}
	if sig.Hop {
		s.Counts.HopNew = 1
	}
	if sig.Spec {
		s.Conformance = []ConformanceFinding{{Kind: "undocumented-status", Method: "GET", Path: "/cart", Status: 418}}
	}
	if sig.Capture {
		s.Capture.B = runs.CaptureTrust{Status: trace.VerdictSuspect, Summary: "unattributed traffic mid-run"}
	}
	return s
}

// allSignalVectors enumerates all 32 moved/same combinations.
func allSignalVectors() []TriageSignals {
	var out []TriageSignals
	for i := 0; i < 32; i++ {
		out = append(out, TriageSignals{
			Pixel:   i&1 != 0,
			Wire:    i&2 != 0,
			Hop:     i&4 != 0,
			Spec:    i&8 != 0,
			Capture: i&16 != 0,
		})
	}
	return out
}

func TestTheFixtureProducesTheSignalsItClaims(t *testing.T) {
	for _, sig := range allSignalVectors() {
		got := signalsOf(summaryWithSignals(sig, "changed"))
		if got != sig {
			t.Errorf("summaryWithSignals(%+v) derives %+v — the fixture and signalsOf disagree, so every table assertion in this file is classifying a vector nobody asked for", sig, got)
		}
	}
}

// --- the built-in table ---------------------------------------------------

// TestTheFiveDefaultsFromTheBrief writes each of the brief's five rows out
// literally, with the label spelled rather than computed. The exhaustive test
// below covers precedence; this one covers the labels themselves, which a
// coordinated rename in triage.go would otherwise carry along unnoticed.
func TestTheFiveDefaultsFromTheBrief(t *testing.T) {
	for _, tc := range []struct {
		name  string
		sig   TriageSignals
		label string
		rule  string
	}{
		{"pixel only", TriageSignals{Pixel: true}, "client-ui", "pixel-only"},
		{"wire moved", TriageSignals{Wire: true}, "client-behavior", "wire-moved"},
		{"hop only", TriageSignals{Hop: true}, "stack", "hop-only"},
		{"spec with all else same", TriageSignals{Spec: true}, "contract-drift", "spec-only"},
		{"capture not ok", TriageSignals{Capture: true}, "harness", "capture-not-ok"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := triageOf(summaryWithSignals(tc.sig, "changed"), nil)
			if got.Label != tc.label || got.Rule != tc.rule {
				t.Errorf("triage = %s (%s), want %s (%s)", got.Label, got.Rule, tc.label, tc.rule)
			}
		})
	}
}

// TestPrecedenceIsCaptureWireHopSpecPixel walks all 32 vectors against a
// statement of the rule written independently of the table: the first signal
// that moved, in that order, names the label. Reordering any two rows in
// defaultTriageRules, or dropping a "same" constraint that keeps the rows
// mutually exclusive, turns this red.
func TestPrecedenceIsCaptureWireHopSpecPixel(t *testing.T) {
	firstMoved := func(sig TriageSignals) string {
		switch {
		case sig.Capture:
			return TriageHarness
		case sig.Wire:
			return TriageClientBehavior
		case sig.Hop:
			return TriageStack
		case sig.Spec:
			return TriageContractDrift
		case sig.Pixel:
			return TriageClientUI
		}
		return TriageNone
	}
	for _, sig := range allSignalVectors() {
		// "pass" so the no-signal vector lands on TriageNone rather than
		// TriageUnclassified; every other vector's label is verdict-blind.
		got := triageOf(summaryWithSignals(sig, "pass"), nil)
		if want := firstMoved(sig); got.Label != want {
			t.Errorf("signals %+v classified %q by rule %q, want %q", sig, got.Label, got.Rule, want)
		}
		if got.Signals != sig {
			t.Errorf("signals %+v reported back as %+v — the vector on the wire is not the vector that was classified", sig, got.Signals)
		}
	}
}

// TestTheBuiltInRowsAreMutuallyExclusiveAndTotal pins the property the table
// claims for itself, which first-match order alone does NOT give it: exactly
// one built-in row matches every vector but the all-clean one.
//
// Order makes the OUTPUT right; exclusivity makes each row READABLE. A reader
// looking at "hop-only" should be able to say when it fires without holding
// the four rows above it in their head, and a project rule interleaved into
// the list must not silently change which default fires below it. Both stop
// being true the moment two rows can match the same vector.
//
// This is the only assertion that fails when a redundant-looking `same`
// constraint is deleted — every label stays correct, because the row above
// still matches first. That is exactly why the constraint would get deleted.
func TestTheBuiltInRowsAreMutuallyExclusiveAndTotal(t *testing.T) {
	for _, sig := range allSignalVectors() {
		var matched []string
		for _, r := range defaultTriageRules {
			if matches(r, sig) {
				matched = append(matched, r.Name)
			}
		}
		want := 1
		if sig == (TriageSignals{}) {
			want = 0 // nothing moved: triageOf's own none/unclassified split
		}
		if len(matched) != want {
			t.Errorf("signals %+v matched %d built-in rows %v, want %d — overlapping rows mean a row cannot be read on its own, and a rule inserted above one changes which of the others fires",
				sig, len(matched), matched, want)
		}
	}
}

// TestEveryVectorGetsALabel is the zero-value guard for this field. An empty
// label is a sixth meaning no consumer can read, and it is what a table with
// a hole in it produces.
func TestEveryVectorGetsALabel(t *testing.T) {
	for _, verdict := range []string{"pass", "changed", "failed"} {
		for _, sig := range allSignalVectors() {
			got := triageOf(summaryWithSignals(sig, verdict), nil)
			if got.Label == "" || got.Rule == "" {
				t.Errorf("verdict %q signals %+v produced label %q rule %q — the table has a hole", verdict, sig, got.Label, got.Rule)
			}
		}
	}
}

// TestNothingMovedIsNotTheSameAsNothingWrong is the whole reason there are
// two no-signal labels. A perf budget, an unexpected status, a hopRequire
// route and an unevaluated gate all fail a run without moving any of the five
// signals; reporting those as "none" would be a clean-looking classification
// on the run that most needs reading.
func TestNothingMovedIsNotTheSameAsNothingWrong(t *testing.T) {
	quiet := TriageSignals{}
	if got := triageOf(summaryWithSignals(quiet, "pass"), nil); got.Label != TriageNone {
		t.Errorf("a clean run classified %q, want %q", got.Label, TriageNone)
	}
	for _, verdict := range []string{"changed", "failed"} {
		got := triageOf(summaryWithSignals(quiet, verdict), nil)
		if got.Label != TriageUnclassified {
			t.Errorf("verdict %q with no signal moved classified %q, want %q — a failure the signals do not cover must not read as a clean run", verdict, got.Label, TriageUnclassified)
		}
	}
}

// --- signal derivation ----------------------------------------------------

// TestEveryClauseOfEverySignalMovesIt walks each individual field a signal
// reads. summaryWithSignals drives one clause per signal — WireChanged,
// HopNew, a "changed" checkpoint, side B's capture — so without this table a
// mutation that deleted any OTHER clause would survive every test above with
// nothing red. WireMoved/Missing/Extra, GoneRoutes, the three non-"ok"
// checkpoint verdicts and side A's capture are all reachable in production and
// all silent otherwise.
func TestEveryClauseOfEverySignalMovesIt(t *testing.T) {
	base := func() Summary {
		return Summary{Verdict: "changed", Capture: CaptureBanner{A: okCapture(), B: okCapture()}}
	}
	notOK := runs.CaptureTrust{Status: trace.VerdictBroken, Summary: "the proxy died mid-run"}
	for _, tc := range []struct {
		name   string
		mut    func(*Summary)
		signal func(TriageSignals) bool
		which  string
	}{
		{"wire moved", func(s *Summary) { s.Counts.WireMoved = 1 }, func(g TriageSignals) bool { return g.Wire }, "wire"},
		{"wire missing", func(s *Summary) { s.Counts.WireMissing = 1 }, func(g TriageSignals) bool { return g.Wire }, "wire"},
		{"wire extra", func(s *Summary) { s.Counts.WireExtra = 1 }, func(g TriageSignals) bool { return g.Wire }, "wire"},
		// The unattributable backstop: the plane counted a changed entry and
		// no scoped evidence explained it. It must not fall through to "no
		// signal moved" — a changed run reporting `unclassified` is a worse
		// answer than an imprecise one.
		{"wire changed with no scoped evidence", func(s *Summary) { s.Counts.WireChanged = 1 }, func(g TriageSignals) bool { return g.Wire }, "wire"},
		// --- the scope split. Each of these is the SAME plane movement
		// attributed to opposite causes, which is the whole point.
		{"request body changed", func(s *Summary) {
			s.Wire.Paired = []Entry{{Method: "POST", NormalizedPath: "/cart", BodyDiff: []FieldDiff{{Scope: "req", Path: "sku"}}}}
		}, func(g TriageSignals) bool { return g.Wire && !g.Hop }, "wire"},
		{"response body changed", func(s *Summary) {
			s.Wire.Paired = []Entry{{Method: "POST", NormalizedPath: "/cart", BodyDiff: []FieldDiff{{Scope: "resp", Path: "total"}}}}
		}, func(g TriageSignals) bool { return g.Hop && !g.Wire }, "hop"},
		{"request header changed", func(s *Summary) {
			s.Wire.Paired = []Entry{{HeaderDiff: []HeaderDiff{{Scope: "req", Name: "x-flag", Type: "changed"}}}}
		}, func(g TriageSignals) bool { return g.Wire && !g.Hop }, "wire"},
		{"response header changed", func(s *Summary) {
			s.Wire.Paired = []Entry{{HeaderDiff: []HeaderDiff{{Scope: "resp", Name: "x-flag", Type: "changed"}}}}
		}, func(g TriageSignals) bool { return g.Hop && !g.Wire }, "hop"},
		{"a request body rule violation", func(s *Summary) {
			s.Wire.Paired = []Entry{{BodyViolations: []FieldDiff{{Scope: "req", Path: "sku"}}}}
		}, func(g TriageSignals) bool { return g.Wire && !g.Hop }, "wire"},
		{"an ordering change in a response", func(s *Summary) {
			s.Wire.Paired = []Entry{{OrderingChanges: []FieldDiff{{Scope: "resp", Path: "items"}}}}
		}, func(g TriageSignals) bool { return g.Hop && !g.Wire }, "hop"},
		{"the status changed", func(s *Summary) {
			s.Wire.Paired = []Entry{{StatusChange: &StatusChange{A: 200, B: 500}}}
		}, func(g TriageSignals) bool { return g.Hop && !g.Wire }, "hop"},
		{"the call moved in the sequence", func(s *Summary) {
			s.Wire.Paired = []Entry{{Moved: true}}
		}, func(g TriageSignals) bool { return g.Wire && !g.Hop }, "wire"},
		{"hop new", func(s *Summary) { s.Counts.HopNew = 1 }, func(g TriageSignals) bool { return g.Hop }, "hop"},
		{"hop gone", func(s *Summary) { s.Counts.HopGone = 1 }, func(g TriageSignals) bool { return g.Hop }, "hop"},
		{"service count deviates", func(s *Summary) {
			s.Hops.ServiceCounts = []ServiceCount{{Service: "cart", Deviates: true}}
		}, func(g TriageSignals) bool { return g.Hop }, "hop"},
		{"checkpoint changed", func(s *Summary) {
			s.Checkpoints = []CheckpointVerdict{{Name: "cart", Verdict: "changed"}}
		}, func(g TriageSignals) bool { return g.Pixel }, "pixel"},
		{"checkpoint missing", func(s *Summary) {
			s.Checkpoints = []CheckpointVerdict{{Name: "cart", Verdict: "missing"}}
		}, func(g TriageSignals) bool { return g.Pixel }, "pixel"},
		{"checkpoint added", func(s *Summary) {
			s.Checkpoints = []CheckpointVerdict{{Name: "cart", Verdict: "added"}}
		}, func(g TriageSignals) bool { return g.Pixel }, "pixel"},
		{"checkpoint unreadable", func(s *Summary) {
			s.Checkpoints = []CheckpointVerdict{{Name: "cart", Verdict: "unreadable"}}
		}, func(g TriageSignals) bool { return g.Pixel }, "pixel"},
		{"capture side a not ok", func(s *Summary) { s.Capture.A = notOK }, func(g TriageSignals) bool { return g.Capture }, "capture"},
		{"capture side b not ok", func(s *Summary) { s.Capture.B = notOK }, func(g TriageSignals) bool { return g.Capture }, "capture"},
		{"quarantined", func(s *Summary) {
			s.Quarantined = []Quarantine{{Side: "a", Reason: "truncated"}}
		}, func(g TriageSignals) bool { return g.Capture }, "capture"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := base()
			tc.mut(&s)
			if !tc.signal(signalsOf(s)) {
				t.Errorf("%s left the %s signal false — this clause is reachable in production and nothing else in this file covers it", tc.name, tc.which)
			}
		})
	}
	// ...and the negative side: an all-clean Summary moves nothing. Without
	// it, a signalsOf that returned all-true would satisfy every row above.
	if got := signalsOf(base()); got != (TriageSignals{}) {
		t.Errorf("a clean Summary derived %+v, want every signal false", got)
	}

	// A difference somebody excused in writing is not evidence of a cause —
	// the same rule Counts already applies to a call an approved deviation
	// covers. These three are the ones a scope-reading loop can most easily
	// sweep up, because they sit on the same Entry as the real findings.
	for _, tc := range []struct {
		name string
		mut  func(*Summary)
	}{
		{"a tolerated header change", func(s *Summary) {
			s.Wire.Paired = []Entry{{HeaderDiff: []HeaderDiff{{Scope: "resp", Name: "date", Type: "tolerated"}}}}
		}},
		{"a tolerated body field", func(s *Summary) {
			s.Wire.Paired = []Entry{{BodyTolerated: []FieldDiff{{Scope: "resp", Path: "updatedAt"}}}}
		}},
		{"an ignored body field", func(s *Summary) {
			s.Wire.Paired = []Entry{{BodyIgnored: []FieldDiff{{Scope: "resp", Path: "requestId"}}}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := base()
			tc.mut(&s)
			if got := signalsOf(s); got != (TriageSignals{}) {
				t.Errorf("%s derived %+v — a difference a rule excused in writing must not attribute a cause", tc.name, got)
			}
		})
	}
}

// TestAnUncheckedConformanceFindingIsNotContractDrift pins the one place the
// spec signal deliberately disagrees with "there is a finding". An
// "unchecked" finding means the required-field check could not run — the
// absence of evidence, not drift — and labelling it contract-drift would
// send a reader to argue with an OpenAPI file that never changed.
func TestAnUncheckedConformanceFindingIsNotContractDrift(t *testing.T) {
	s := Summary{Verdict: "pass", Capture: CaptureBanner{A: okCapture(), B: okCapture()}}
	s.Conformance = []ConformanceFinding{{Kind: "unchecked", Method: "GET", Path: "/cart", Detail: "body was truncated at capture"}}
	if got := signalsOf(s); got.Spec {
		t.Error("an unchecked conformance finding set the spec signal — unchecked is the absence of a verdict, not a contract change")
	}
}

// TestADeviatingServiceCountMovesTheHopSignal covers the half of the hop
// signal that is not NewRoutes/GoneRoutes. A stack that answers the same
// routes a different NUMBER of times is exactly the "stack" case, and reading
// only the route lists misses it.
func TestADeviatingServiceCountMovesTheHopSignal(t *testing.T) {
	s := Summary{Verdict: "changed", Capture: CaptureBanner{A: okCapture(), B: okCapture()}}
	s.Hops.ServiceCounts = []ServiceCount{{Service: "cart", A: 2, B: 9, Deviates: true}}
	if got := signalsOf(s); !got.Hop {
		t.Error("a deviating service count left the hop signal false — a stack that answers the same routes a different number of times is still the stack")
	}
	if got := triageOf(s, nil); got.Label != TriageStack {
		t.Errorf("classified %q, want %q", got.Label, TriageStack)
	}
}

// TestATruncatedRunIsAHarnessProblemEvenWhenItsCaptureVerdictIsOk is the
// clause that makes the capture signal read `Quarantined` and not just the
// trust verdict. incompleteCheck quarantines a signal-killed run whose
// capture verdict is a perfectly ok "ok"; without the quarantine clause that
// run reports an all-false vector, and an all-false vector on a
// non-pass verdict is "unclassified" — technically not wrong, and useless.
func TestATruncatedRunIsAHarnessProblemEvenWhenItsCaptureVerdictIsOk(t *testing.T) {
	s := Summary{
		Verdict:     "quarantined",
		Capture:     CaptureBanner{A: okCapture(), B: okCapture()},
		Quarantined: []Quarantine{{Side: "b", Reason: "the test command did not complete (signal-killed, raw exit code -1)"}},
	}
	if got := signalsOf(s); !got.Capture {
		t.Error("a quarantined run whose capture verdict is ok left the capture signal false")
	}
	if got := triageOf(s, nil); got.Label != TriageHarness {
		t.Errorf("classified %q, want %q", got.Label, TriageHarness)
	}
}

// --- project overrides ----------------------------------------------------

func TestAProjectRuleIsConsultedBeforeTheDefaults(t *testing.T) {
	cfg := &config.Config{Triage: []config.TriageRule{{
		Name:  "seed-drift",
		Label: "seeds",
		When:  config.TriageWhen{Hop: config.TriageMoved, Wire: config.TriageSame},
		Why:   "our hop plane moves whenever the fixture seed is regenerated",
	}}}
	sig := TriageSignals{Hop: true}
	got := triageOf(summaryWithSignals(sig, "changed"), cfg)
	if got.Label != "seeds" || got.Rule != "seed-drift" {
		t.Errorf("triage = %s (%s), want seeds (seed-drift) — a project rule must be consulted before the built-in table", got.Label, got.Rule)
	}
	// ...and the defaults are still there underneath for everything the
	// project rule does not match. A config that REPLACED the table would
	// most often lose `harness`, which is the misread this ordering protects.
	if got := triageOf(summaryWithSignals(TriageSignals{Capture: true}, "changed"), cfg); got.Label != TriageHarness {
		t.Errorf("with a project rule configured, a not-ok capture classified %q, want %q", got.Label, TriageHarness)
	}
}

// TestAQuarantineIsNotOverridable is the one exemption from the rule above.
// Build returns from a quarantine before a single plane is computed, so the
// four traffic signals are false for want of DATA rather than for want of
// differences — and a project rule matching "wire: same, pixel: same" would
// happily relabel a comparison that never happened.
func TestAQuarantineIsNotOverridable(t *testing.T) {
	cfg := &config.Config{Triage: []config.TriageRule{{
		Name:  "greedy",
		Label: "ours",
		When:  config.TriageWhen{Wire: config.TriageSame, Pixel: config.TriageSame},
	}}}
	s := Summary{
		Verdict:     "quarantined",
		Capture:     CaptureBanner{A: okCapture(), B: okCapture()},
		Quarantined: []Quarantine{{Side: "a", Reason: "proxy died mid-run"}},
	}
	got := triageOf(s, cfg)
	if got.Label != TriageHarness || got.Rule != "quarantined" {
		t.Errorf("triage = %s (%s), want harness (quarantined) — nothing was compared, so there is nothing for a project rule to classify", got.Label, got.Rule)
	}
}

// TestARuleThatConstrainsNothingMatchesNothing is the belt to validateTriage's
// braces. Load rejects an empty `when:`, so this state should be unreachable
// — but if the two ever disagree, a rule that matched EVERY run would put one
// label on every diff in the project, including the quarantined ones.
func TestARuleThatConstrainsNothingMatchesNothing(t *testing.T) {
	empty := config.TriageRule{Name: "everything", Label: "ours"}
	if matches(empty, TriageSignals{Wire: true}) {
		t.Error("a rule with no constraints matched — an unconstrained rule shadows every rule below it, including the built-in harness row")
	}
}

// TestARuleNamingAnUnknownSignalMatchesNothing pins the direction of the
// failure for a signal name this build does not know: refuse, never treat it
// as unconstrained. Treating it as unconstrained would make a rule get
// BROADER as the constraint it names becomes less understood.
func TestARuleNamingAnUnknownSignalMatchesNothing(t *testing.T) {
	if moved, known := signalOf(TriageSignals{Wire: true, Pixel: true}, "network"); moved || known {
		t.Errorf("signalOf(unknown) = (%v, %v), want (false, false)", moved, known)
	}
}

// --- wiring through Build -------------------------------------------------

// TestBuildClassifiesARealPixelOnlyChange is the end-to-end proof that Build
// calls the classifier at all. Every test above operates on a hand-built
// Summary and would stay green with the triage field never populated.
func TestBuildClassifiesARealPixelOnlyChange(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	dark := color.RGBA{R: 10, G: 20, B: 30, A: 255}
	red := color.RGBA{R: 250, G: 0, B: 0, A: 255}
	base := solidPNG(t, 20, 20, dark)
	changed := rectPNG(t, 20, 20, dark, 0, 0, 10, 10, red)
	cpA := writeShot(t, dirA, "cart", base)
	cpB := writeShot(t, dirB, "cart", changed)
	hop := trace.Hop{Seq: 1, Method: "GET", Path: "/cart", Status: 200}
	writeWireFile(t, dirA, []trace.Hop{hop})
	writeWireFile(t, dirB, []trace.Hop{hop})

	cfg := baseConfig(t)
	s := mustBuild(t, BuildInput{
		App: "app", Flow: "flow", Cfg: cfg,
		A: RunRef{Kind: "run", Dir: dirA, Manifest: manifest("a", []runs.Checkpoint{cpA}, nil, okCapture())},
		B: RunRef{Kind: "run", Dir: dirB, Manifest: manifest("b", []runs.Checkpoint{cpB}, nil, okCapture())},
	})
	if s.Triage.Label != TriageClientUI {
		t.Errorf("Triage = %+v, want label %q — Build did not classify a real pixel-only change", s.Triage, TriageClientUI)
	}
	if !s.Triage.Signals.Pixel || s.Triage.Signals.Wire {
		t.Errorf("Triage.Signals = %+v, want pixel moved and wire same", s.Triage.Signals)
	}
}

// TestBuildAttributesAWireChangeToTheSideThatActuallyMoved is the case that
// found the defect. Running the real CLI against a stack whose RESPONSE body
// changed printed "TRIAGE: client-behavior — the client is making different
// calls" over a client that had made the byte-identical request.
//
// A backend returning different data is the most common real change there is,
// and on a standalone run — no hops.jsonl at all — the response half of the
// wire plane is the only evidence that it happened.
func TestBuildAttributesAWireChangeToTheSideThatActuallyMoved(t *testing.T) {
	get := func(reqBody, respBody string, status int) trace.Hop {
		return trace.Hop{
			Seq: 1, Method: "POST", Path: "/cart", Status: status,
			Req:  trace.Payload{Body: reqBody},
			Resp: trace.Payload{Body: respBody},
		}
	}
	for _, tc := range []struct {
		name string
		a, b trace.Hop
		want string
	}{{
		name: "the client sent a different body",
		a:    get(`{"sku":"a"}`, `{"total":10}`, 200),
		b:    get(`{"sku":"b"}`, `{"total":10}`, 200),
		want: TriageClientBehavior,
	}, {
		name: "the stack answered with a different body",
		a:    get(`{"sku":"a"}`, `{"total":10}`, 200),
		b:    get(`{"sku":"a"}`, `{"total":99}`, 200),
		want: TriageStack,
	}, {
		name: "the stack answered with a different status",
		a:    get(`{"sku":"a"}`, `{"total":10}`, 200),
		b:    get(`{"sku":"a"}`, `{"total":10}`, 500),
		want: TriageStack,
	}} {
		t.Run(tc.name, func(t *testing.T) {
			dirA, dirB := t.TempDir(), t.TempDir()
			writeWireFile(t, dirA, []trace.Hop{tc.a})
			writeWireFile(t, dirB, []trace.Hop{tc.b})
			s := mustBuild(t, BuildInput{
				App: "app", Flow: "flow", Cfg: baseConfig(t),
				A: RunRef{Kind: "run", Dir: dirA, Manifest: manifest("a", nil, nil, okCapture())},
				B: RunRef{Kind: "run", Dir: dirB, Manifest: manifest("b", nil, nil, okCapture())},
			})
			if s.Verdict == "pass" {
				t.Fatalf("test setup: nothing was detected as changed, so this case classifies an unchanged run: %+v", s.Counts)
			}
			if s.Triage.Label != tc.want {
				t.Errorf("Triage = %+v, want label %q", s.Triage, tc.want)
			}
		})
	}
}

// TestEveryBuildExitCarriesATriageLabel walks all three of Build's returning
// exits — the ordinary one and both quarantines. The quarantine exits are the
// ones a `finish` helper exists to protect: they compute almost nothing, so
// they are where an unset field is most likely to ship.
func TestEveryBuildExitCarriesATriageLabel(t *testing.T) {
	newRun := func(t *testing.T) string {
		t.Helper()
		dir := t.TempDir()
		writeWireFile(t, dir, []trace.Hop{{Seq: 1, Method: "GET", Path: "/cart", Status: 200}})
		return dir
	}
	for _, tc := range []struct {
		name  string
		mutB  func(m *runs.Manifest)
		want  string
		vrdct string
	}{
		{"ordinary", func(*runs.Manifest) {}, TriageNone, "pass"},
		{"truncated recording", func(m *runs.Manifest) { m.Test.ExitCode = -1 }, TriageHarness, "quarantined"},
		{"untrusted capture", func(m *runs.Manifest) {
			m.Capture = runs.CaptureTrust{Status: trace.VerdictBroken, Summary: "the proxy died mid-run"}
		}, TriageHarness, "quarantined"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dirA, dirB := newRun(t), newRun(t)
			mB := manifest("b", nil, nil, okCapture())
			tc.mutB(&mB)
			s := mustBuild(t, BuildInput{
				App: "app", Flow: "flow", Cfg: baseConfig(t),
				A: RunRef{Kind: "run", Dir: dirA, Manifest: manifest("a", nil, nil, okCapture())},
				B: RunRef{Kind: "run", Dir: dirB, Manifest: mB},
			})
			if s.Verdict != tc.vrdct {
				t.Fatalf("Verdict = %q, want %q — this case is not exercising the exit it names", s.Verdict, tc.vrdct)
			}
			if s.Triage.Label != tc.want {
				t.Errorf("Triage = %+v, want label %q", s.Triage, tc.want)
			}
		})
	}
}

// --- the text report ------------------------------------------------------

func TestRenderTextNamesTheTriageAndItsEvidence(t *testing.T) {
	var b bytes.Buffer
	s := summaryWithSignals(TriageSignals{Wire: true, Pixel: true}, "changed")
	s.Triage = triageOf(s, nil)
	s.ensureArrays()
	RenderText(&b, s)
	out := b.String()
	for _, want := range []string{
		"TRIAGE: client-behavior (wire-moved)",
		// The "so what" clause. A label is a category name; a reader who has
		// not read the recipe still needs to be told where to look, and this
		// report is the surface most often read without it.
		"the client sent something different",
		"signals moved: wire, pixel",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report does not contain %q:\n%s", want, out)
		}
	}
}

// TestRenderTextSaysNothingAboutTriageOnAHandBuiltSummary covers renderTriage's
// guard. Summaries assembled in tests and by callers that never went through
// Build carry an empty label, and "TRIAGE:  ()" is a line that looks like a
// classification and carries none — worse than the silence it replaced.
func TestRenderTextSaysNothingAboutTriageOnAHandBuiltSummary(t *testing.T) {
	var b bytes.Buffer
	s := Summary{Verdict: "pass", Capture: CaptureBanner{A: okCapture(), B: okCapture()}}
	s.ensureArrays()
	RenderText(&b, s)
	if out := b.String(); strings.Contains(out, "TRIAGE:") {
		t.Errorf("a Summary with no triage label printed a triage line:\n%s", out)
	}
}

// TestRenderTextNamesTheTriageOnAQuarantine covers the early return. A
// quarantine is the verdict readers most often mistake for a small failure
// and go looking for in their application code, so it is the one report that
// most needs the line — and the one whose code path skips every other
// section.
func TestRenderTextNamesTheTriageOnAQuarantine(t *testing.T) {
	var b bytes.Buffer
	s := Summary{
		Verdict:     "quarantined",
		Capture:     CaptureBanner{A: okCapture(), B: okCapture()},
		Quarantined: []Quarantine{{Side: "b", Reason: "the proxy died mid-run"}},
	}
	s.Triage = triageOf(s, nil)
	s.ensureArrays()
	RenderText(&b, s)
	if out := b.String(); !strings.Contains(out, "TRIAGE: harness (quarantined)") {
		t.Errorf("a quarantined report never named its triage:\n%s", out)
	}
}

// TestTriageLabelsCoversEveryLabelTheClassifierEmits keeps the exported list
// honest by WALKING the classifier rather than restating it: every label
// triageOf can produce, across every vector and every verdict, must appear.
// The docs contract test checks the recipe against this list, so a label
// missing here is a label an agent meets for the first time in production.
func TestTriageLabelsCoversEveryLabelTheClassifierEmits(t *testing.T) {
	declared := map[string]bool{}
	for _, l := range TriageLabels() {
		declared[l] = true
	}
	emitted := map[string]bool{}
	for _, verdict := range []string{"pass", "changed", "failed", "quarantined"} {
		for _, sig := range allSignalVectors() {
			s := summaryWithSignals(sig, verdict)
			if verdict == "quarantined" {
				s.Quarantined = []Quarantine{{Side: "a", Reason: "fixture"}}
			}
			label := triageOf(s, nil).Label
			emitted[label] = true
			if !declared[label] {
				t.Errorf("triageOf emits %q for verdict %q signals %+v, and TriageLabels() does not list it", label, verdict, sig)
			}
		}
	}
	// ...and the other direction, or the list could name labels nothing
	// produces and the docs would document a value no agent will ever see.
	for l := range declared {
		if !emitted[l] {
			t.Errorf("TriageLabels() names %q, which no vector produces", l)
		}
	}
}
