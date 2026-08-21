package diff

import (
	"testing"

	"github.com/caribou-crew/ensemble/core/trace"
)

func hopWithDoneMs(seq uint64, ms float64) trace.Hop {
	return trace.Hop{Seq: seq, T: trace.Timings{DoneMs: ms}}
}

// TestPerfBudgetEndToEndFromSamplesToVerdict drives all three exported
// perf.go functions together, the way Task 10 will: derive a budget from
// historical samples, then check a fresh run's total against it.
func TestPerfBudgetEndToEndFromSamplesToVerdict(t *testing.T) {
	samples := []float64{100, 120, 90, 150} // max = 150

	budget, err := DerivePerfBudget(samples, 0)
	if err != nil {
		t.Fatalf("DerivePerfBudget: %v", err)
	}
	wantBudget := 150 * defaultPerfMarginFactor
	if budget.BudgetMs != wantBudget {
		t.Fatalf("BudgetMs = %v, want %v", budget.BudgetMs, wantBudget)
	}

	okHops := []trace.Hop{hopWithDoneMs(1, 100), hopWithDoneMs(2, 50)} // sum = 150
	okResult := CheckPerfBudget(okHops, budget.BudgetMs)
	if okResult.Status != "ok" {
		t.Fatalf("150ms against a %vms budget: status = %q, want ok", budget.BudgetMs, okResult.Status)
	}

	overHops := []trace.Hop{hopWithDoneMs(1, 200), hopWithDoneMs(2, 100)} // sum = 300 > 225
	overResult := CheckPerfBudget(overHops, budget.BudgetMs)
	if overResult.Status != "over" {
		t.Fatalf("300ms against a %vms budget: status = %q, want over", budget.BudgetMs, overResult.Status)
	}
}

func TestTotalIsASumNotAMedian(t *testing.T) {
	// Deliberately asymmetric values: sum (120), median (20) and mean
	// (40) are all different, so any of the three being computed instead
	// of the sum is caught.
	hops := []trace.Hop{
		hopWithDoneMs(1, 10),
		hopWithDoneMs(2, 20),
		hopWithDoneMs(3, 90),
	}

	got := TotalCallDurationMs(hops)
	if got != 120 {
		t.Fatalf("TotalCallDurationMs = %v, want 120 (a sum, not a median or a mean)", got)
	}
}

func TestDeriveBudgetUsesObservedMaxTimesMargin(t *testing.T) {
	samples := []float64{10, 50, 30} // max = 50, median = 30 — distinct on purpose

	budget, err := DerivePerfBudget(samples, 2)
	if err != nil {
		t.Fatalf("DerivePerfBudget: %v", err)
	}
	if budget.MeasuredMaxMs != 50 {
		t.Errorf("MeasuredMaxMs = %v, want 50", budget.MeasuredMaxMs)
	}
	if budget.MeasuredMedianMs != 30 {
		t.Errorf("MeasuredMedianMs = %v, want 30 (not fed into BudgetMs)", budget.MeasuredMedianMs)
	}
	if budget.BudgetMs != 100 {
		t.Errorf("BudgetMs = %v, want 100 (max 50 * margin 2, not median 30 * margin)", budget.BudgetMs)
	}
	if budget.SampleCount != 3 {
		t.Errorf("SampleCount = %d, want 3", budget.SampleCount)
	}
}

func TestDeriveBudgetMarginFactorZeroFallsBackToDefault(t *testing.T) {
	budget, err := DerivePerfBudget([]float64{100}, 0)
	if err != nil {
		t.Fatalf("DerivePerfBudget: %v", err)
	}
	if budget.MarginFactor != defaultPerfMarginFactor || budget.BudgetMs != 100*defaultPerfMarginFactor {
		t.Fatalf("marginFactor:0 must fall back to the default 1.5x; got %+v", budget)
	}
}

func TestDeriveBudgetMarginFactorExplicitValueIsHonoredNotOverridden(t *testing.T) {
	budget, err := DerivePerfBudget([]float64{100}, 2)
	if err != nil {
		t.Fatalf("DerivePerfBudget: %v", err)
	}
	if budget.MarginFactor != 2 || budget.BudgetMs != 200 {
		t.Fatalf("an explicit marginFactor of 2 must be honored (not defaulted to 1.5); got %+v", budget)
	}
}

func TestDeriveBudgetRejectsAnEmptySample(t *testing.T) {
	_, err := DerivePerfBudget(nil, 1.5)
	if err == nil {
		t.Fatal("DerivePerfBudget(nil, ...) must return an error, not a silent zero budget")
	}
}

func TestAnUnsetBudgetReportsUnsetNotOk(t *testing.T) {
	// Nonzero measured time so a naive "measured <= budget" comparison
	// against a zero budget can't accidentally read as "ok" or "over" —
	// unset must be its own status, checked before any comparison.
	hops := []trace.Hop{hopWithDoneMs(1, 50)}

	got := CheckPerfBudget(hops, 0)
	if got.Status != "unset" {
		t.Fatalf("CheckPerfBudget with budgetMs=0: status = %q, want unset", got.Status)
	}
	if got.MeasuredMs != 50 {
		t.Fatalf("MeasuredMs must still be reported even when unset: got %v, want 50", got.MeasuredMs)
	}
}

// TestTotalCallDurationMsFoldsRelaysSoTheOuterLegIsNotDoubleCounted pins
// I1: core/trace's collapse code documents that a relay's outer leg's
// wall clock already CONTAINS the inner leg's (MergeForDetail: "the
// outer leg's wall clock contains the inner"). Summing every raw hop's
// DoneMs therefore double-counts every relayed call. Two topologies
// representing the SAME one logical call — one direct, one through a
// transparent relay — must fold to the same total.
func TestTotalCallDurationMsFoldsRelaysSoTheOuterLegIsNotDoubleCounted(t *testing.T) {
	direct := []trace.Hop{
		{Seq: 1, TraceID: "t1", From: "client", To: "bff", Method: "GET", Path: "/x", Status: 200, T: trace.Timings{DoneMs: 100}},
	}
	relayed := []trace.Hop{
		// The outer (client->edge) leg's DoneMs already contains the
		// inner (edge->bff) leg's — per the doc comment above, this is
		// the SAME logical call as `direct`, just observed through one
		// relay hop.
		{Seq: 1, TraceID: "t1", From: "client", To: "edge", Method: "GET", Path: "/x", Status: 200, T: trace.Timings{DoneMs: 100}},
		{Seq: 2, TraceID: "t1", From: "edge", To: "bff", Method: "GET", Path: "/x", Status: 200, T: trace.Timings{DoneMs: 45}},
	}

	directTotal := TotalCallDurationMs(direct)
	relayedTotal := TotalCallDurationMs(relayed)
	if relayedTotal != directTotal {
		t.Fatalf("folded totals must match: direct=%v relayed=%v (unfolded, relayed would wrongly be %v)",
			directTotal, relayedTotal, direct[0].T.DoneMs+relayed[1].T.DoneMs)
	}
	if relayedTotal != 100 {
		t.Fatalf("TotalCallDurationMs(relayed) = %v, want 100 (the outer leg only, not 100+45)", relayedTotal)
	}
}

func TestDeriveBudgetNegativeMarginFactorIsNotDefaulted(t *testing.T) {
	// Only an EXACT zero should default — a negative value must flow
	// through unchanged (even though it produces a budget CheckPerfBudget
	// will report as "over" for every run; validating the config value
	// is a caller concern, not this function's). Mirrors the
	// zero-vs-negative distinction CountTolerance already pins in hop.go.
	budget, err := DerivePerfBudget([]float64{100}, -1)
	if err != nil {
		t.Fatalf("DerivePerfBudget: %v", err)
	}
	if budget.MarginFactor != -1 || budget.BudgetMs != -100 {
		t.Fatalf("a negative marginFactor must not be defaulted; got %+v", budget)
	}
}

func TestDeriveBudgetMedianAveragesTheMiddleTwoOnAnEvenSample(t *testing.T) {
	budget, err := DerivePerfBudget([]float64{10, 20, 30, 40}, 1)
	if err != nil {
		t.Fatalf("DerivePerfBudget: %v", err)
	}
	if budget.MeasuredMedianMs != 25 {
		t.Fatalf("MeasuredMedianMs = %v, want 25 (the average of the middle two, 20 and 30)", budget.MeasuredMedianMs)
	}
}

func TestCheckPerfBudgetBoundaryEqualsOk(t *testing.T) {
	hops := []trace.Hop{hopWithDoneMs(1, 100)}
	got := CheckPerfBudget(hops, 100)
	if got.Status != "ok" {
		t.Fatalf("measured == budget must be ok (over requires strictly exceeding it), got %q", got.Status)
	}
}

func TestPerfResultAndPerfBudgetJSONKeysMatchContract(t *testing.T) {
	assertJSONKeys(t, PerfResult{Status: "ok", MeasuredMs: 1, BudgetMs: 2}, []string{"status", "measuredMs", "budgetMs"})
	assertJSONKeys(t, PerfBudget{BudgetMs: 1, SampleCount: 1, MeasuredMaxMs: 1, MeasuredMedianMs: 1, MarginFactor: 1},
		[]string{"budgetMs", "sampleCount", "measuredMaxMs", "measuredMedianMs", "marginFactor"})
}
