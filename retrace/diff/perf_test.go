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

func TestPerfResultAndPerfBudgetJSONKeysMatchContract(t *testing.T) {
	assertJSONKeys(t, PerfResult{Status: "ok", MeasuredMs: 1, BudgetMs: 2}, []string{"status", "measuredMs", "budgetMs"})
	assertJSONKeys(t, PerfBudget{BudgetMs: 1, SampleCount: 1, MeasuredMaxMs: 1, MeasuredMedianMs: 1, MarginFactor: 1},
		[]string{"budgetMs", "sampleCount", "measuredMaxMs", "measuredMedianMs", "marginFactor"})
}
