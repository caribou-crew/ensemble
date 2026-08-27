package diff

import (
	"strings"
	"testing"

	"github.com/caribou-crew/ensemble/retrace/config"
)

// cpGate builds a pixel gate with a plane budget and per-checkpoint overrides.
func cpGate(budget float64, overrides map[string]float64) config.Gate {
	return config.Gate{BudgetPct: &budget, Checkpoints: overrides}
}

// shots builds a Summary carrying just the checkpoint verdicts a pixel gate
// reads.
func shots(pairs ...any) Summary {
	var s Summary
	for i := 0; i+1 < len(pairs); i += 2 {
		s.Checkpoints = append(s.Checkpoints, CheckpointVerdict{
			Name:    pairs[i].(string),
			DiffPct: pairs[i+1].(float64),
		})
	}
	return s
}

func onlyGate(t *testing.T, s Summary, gates map[string]config.Gate) Gate {
	t.Helper()
	got := budgetsOf(s, gates)
	if len(got) != 1 {
		t.Fatalf("budgetsOf returned %d rows, want 1: %+v", len(got), got)
	}
	return got[0]
}

func TestACheckpointIsJudgedAgainstItsOwnBudget(t *testing.T) {
	// The whole point: cart is allowed 8%, everything else 1.5%.
	gates := map[string]config.Gate{"pixel": cpGate(1.5, map[string]float64{"cart": 8})}

	g := onlyGate(t, shots("cart", 7.0), gates)
	if g.Failed {
		t.Errorf("cart at 7%% failed its own 8%% budget: %+v", g)
	}
	if g.Threshold != 8 || g.Checkpoint != "cart" {
		t.Errorf("row = %+v, want threshold 8 attributed to cart", g)
	}

	g = onlyGate(t, shots("cart", 9.0), gates)
	if !g.Failed {
		t.Errorf("cart at 9%% passed its own 8%% budget: %+v", g)
	}
}

func TestACheckpointWithNoOverrideKeepsThePlaneBudget(t *testing.T) {
	gates := map[string]config.Gate{"pixel": cpGate(1.5, map[string]float64{"cart": 8})}

	g := onlyGate(t, shots("login", 2.0), gates)
	if !g.Failed {
		t.Errorf("login at 2%% passed the plane's 1.5%% budget: %+v", g)
	}
	if g.Threshold != 1.5 {
		t.Errorf("threshold = %v, want the plane's 1.5", g.Threshold)
	}
	if g.Checkpoint != "" {
		t.Errorf("Checkpoint = %q, want empty — the plane's own budget applied, not an override", g.Checkpoint)
	}
}

func TestTheReportedRowIsTheWorstOverageNotTheWorstDiff(t *testing.T) {
	// The reason pixelGate exists. cart has by far the largest DiffPct and is
	// entirely within its budget; login is over. Reporting the largest DiffPct
	// would print a PASSING row over a FAILING run — the single worst outcome
	// available to a CI gate.
	gates := map[string]config.Gate{"pixel": cpGate(1.5, map[string]float64{"cart": 8, "login": 0})}
	g := onlyGate(t, shots("cart", 7.0, "login", 0.4), gates)

	if g.Checkpoint != "login" {
		t.Errorf("reported %q (%.2f%% of %.2f%%), want login — cart has the bigger diff and is within budget", g.Checkpoint, g.Observed, g.Threshold)
	}
	if !g.Failed {
		t.Errorf("the run passed though login blew its 0%% budget: %+v", g)
	}
}

func TestNoOverridesReducesExactlyToTheOldBehaviour(t *testing.T) {
	// Every project that does not use per-checkpoint budgets must get the row
	// it got before they existed — same threshold, same observed (the worst
	// DiffPct), and NO checkpoint attribution in the JSON.
	gates := map[string]config.Gate{"pixel": cpGate(1.5, nil)}
	g := onlyGate(t, shots("cart", 0.4, "login", 1.1, "receipt", 0.2), gates)

	if g.Threshold != 1.5 || g.Observed != 1.1 || g.Failed {
		t.Errorf("row = %+v, want threshold 1.5, observed 1.1 (the worst), not failed", g)
	}
	if g.Checkpoint != "" {
		t.Errorf("Checkpoint = %q, want empty — a project with no overrides must produce byte-identical JSON", g.Checkpoint)
	}
}

func TestTheWorstOverageIsReportedEvenWhenNothingFails(t *testing.T) {
	// A passing run still has to name a real budget. Reporting a threshold
	// from one checkpoint beside an observed from another would be a row true
	// of neither.
	gates := map[string]config.Gate{"pixel": cpGate(2, map[string]float64{"cart": 10})}
	g := onlyGate(t, shots("cart", 1.0, "login", 1.9), gates)

	if g.Failed {
		t.Fatalf("row = %+v, want a pass", g)
	}
	if g.Checkpoint != "" || g.Threshold != 2 || g.Observed != 1.9 {
		t.Errorf("row = %+v, want login's 1.9%% against the plane's 2%% — the closest call in the run", g)
	}
}

func TestAnExplicitZeroCheckpointBudgetGates(t *testing.T) {
	// `checkpoints: {login: 0}` means "this screen must not move at all". If
	// the 0 were read as "absent", login would silently inherit the plane's
	// generous budget — the strictest setting in the file becoming the
	// loosest.
	gates := map[string]config.Gate{"pixel": cpGate(20, map[string]float64{"login": 0})}
	g := onlyGate(t, shots("login", 0.01), gates)
	if !g.Failed || g.Checkpoint != "login" || g.Threshold != 0 {
		t.Errorf("row = %+v, want login failed against a 0%% budget", g)
	}
}

func TestAPixelGateWithNoCheckpointsIsUnmeasurableNotClean(t *testing.T) {
	// Same empty-evidence rule the other planes follow. A max over nothing is
	// 0, which reads as "no pixels changed" on the run with the least
	// evidence in it.
	gates := map[string]config.Gate{"pixel": cpGate(1.5, map[string]float64{"cart": 8})}
	if got := budgetsOf(Summary{}, gates); len(got) != 0 {
		t.Fatalf("budgetsOf on a run with no checkpoints = %+v, want no row", got)
	}
	if got := unmeasuredGatesOf(Summary{}, gates); len(got) != 1 || got[0] != "pixel" {
		t.Errorf("unmeasuredGates = %v, want [pixel] — gated and unmeasurable is not a gate that passed", got)
	}
}

func TestATiedRowDoesNotDependOnCaptureOrder(t *testing.T) {
	// Two checkpoints tied on overage. Without an explicit tiebreak the answer
	// is "whichever the capture happened to enumerate first" — and the order
	// of s.Checkpoints is a filesystem read, not a promise. The same run
	// diffed twice would name a different checkpoint, and a reviewer chasing
	// the one they were shown would find it gone.
	gates := map[string]config.Gate{"pixel": cpGate(1, map[string]float64{"zeta": 5, "alpha": 5})}

	forward := onlyGate(t, shots("alpha", 6.0, "zeta", 6.0), gates)
	reverse := onlyGate(t, shots("zeta", 6.0, "alpha", 6.0), gates)
	if forward != reverse {
		t.Fatalf("capture order changed the reported row: %+v vs %+v", forward, reverse)
	}
	if forward.Checkpoint != "alpha" {
		t.Errorf("tie broke toward %q, want the alphabetically earlier alpha", forward.Checkpoint)
	}
}

func TestAPerCheckpointBudgetDoesNotDisturbTheOtherPlanes(t *testing.T) {
	gates := map[string]config.Gate{
		"pixel": cpGate(1.5, map[string]float64{"cart": 8}),
		"wire":  gatePct(2),
	}
	s := shots("cart", 1.0)
	s.Counts.WirePaired, s.Counts.WireChanged = 100, 5

	var wire *Gate
	for _, g := range budgetsOf(s, gates) {
		if g.Plane == "wire" {
			row := g
			wire = &row
		}
	}
	if wire == nil {
		t.Fatal("the wire gate disappeared once pixel took its own code path")
	}
	if wire.Threshold != 2 || wire.Observed != 5 || !wire.Failed {
		t.Errorf("wire = %+v, want 5%% against a 2%% budget, failed", *wire)
	}
	if wire.Checkpoint != "" {
		t.Errorf("wire carries Checkpoint %q — only pixel has checkpoints", wire.Checkpoint)
	}
}

func TestTheTextReportNamesTheCheckpointWithoutLosingThePlane(t *testing.T) {
	// "BUDGET: cart 8.00% → 9.00% FAILED" would lose which plane failed, and
	// "cart" is not one of the four names every other gate surface is keyed on.
	var b strings.Builder
	s := shots("cart", 9.0)
	s.Verdict = "changed"
	s.Budgets = budgetsOf(s, map[string]config.Gate{"pixel": cpGate(1.5, map[string]float64{"cart": 8})})
	RenderText(&b, s)

	out := b.String()
	if !strings.Contains(out, "BUDGET: pixel (cart)") {
		t.Errorf("the BUDGET row does not name both the plane and the checkpoint:\n%s", out)
	}
}

func TestAnUnattributedRowStillPrintsThePlaneAlone(t *testing.T) {
	// The other half of the line above: a project with no overrides must not
	// start printing an empty parenthesis.
	var b strings.Builder
	s := shots("cart", 0.5)
	s.Verdict = "pass"
	s.Budgets = budgetsOf(s, map[string]config.Gate{"pixel": cpGate(1.5, nil)})
	RenderText(&b, s)

	out := b.String()
	if !strings.Contains(out, "BUDGET: pixel 1.50%") {
		t.Errorf("the BUDGET row changed shape for a project with no overrides:\n%s", out)
	}
	if strings.Contains(out, "pixel ()") {
		t.Errorf("an unattributed row printed an empty parenthesis:\n%s", out)
	}
}

func TestANilConfigGatesNothingRatherThanPanicking(t *testing.T) {
	// Hand-built Summaries reach Build without a config, in tests and in the
	// review UI. "No config" means "no gates", not a crash.
	if got := resolvedGates(nil, "checkout"); got != nil {
		t.Errorf("resolvedGates(nil) = %+v, want nil", got)
	}
	if got := budgetsOf(shots("cart", 9.0), nil); len(got) != 0 {
		t.Errorf("budgetsOf with no gates = %+v, want no rows", got)
	}
	if got := unmeasuredGatesOf(shots("cart", 9.0), nil); len(got) != 0 {
		t.Errorf("unmeasuredGatesOf with no gates = %v, want none", got)
	}
}

func TestBuildResolvesGatesForTheFlowItIsComparing(t *testing.T) {
	// The seam between config and diff. resolvedGates reading cfg.Gates
	// directly — the shape this replaced — leaves every per-flow override
	// inert while every unit test above still passes.
	cfg := baseConfig(t)
	cfg.Gates["pixel"] = cpGate(1.5, nil)
	cfg.Flows = map[string]config.Flow{"checkout": {Gates: map[string]config.Gate{"pixel": cpGate(9, nil)}}}

	if g := resolvedGates(cfg, "checkout")["pixel"]; g.BudgetPct == nil || *g.BudgetPct != 9 {
		t.Errorf("checkout resolved to %+v, want the flow's 9", g.BudgetPct)
	}
	if g := resolvedGates(cfg, "browse")["pixel"]; g.BudgetPct == nil || *g.BudgetPct != 1.5 {
		t.Errorf("browse resolved to %+v, want the global 1.5", g.BudgetPct)
	}
}
