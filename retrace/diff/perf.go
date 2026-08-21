// perf.go checks a run's total backend call time against a budget. The
// budget is derived from observed samples (max, not mean+stddev) because
// dev-machine timings are fat-tailed; a budget tight enough to reject the
// occasional slow outlier gets the whole plane switched off by whoever
// hits it first.
package diff

import (
	"fmt"
	"sort"

	"github.com/caribou-crew/ensemble/core/trace"
)

// PerfResult is CheckPerfBudget's verdict for one run.
type PerfResult struct {
	Status     string  `json:"status"` // "ok" | "over" | "unset"
	MeasuredMs float64 `json:"measuredMs"`
	BudgetMs   float64 `json:"budgetMs"`
}

// PerfBudget is DerivePerfBudget's output: a budget in ms plus the sample
// statistics it was derived from, so a report can show its own working.
type PerfBudget struct {
	BudgetMs         float64 `json:"budgetMs"`
	SampleCount      int     `json:"sampleCount"`
	MeasuredMaxMs    float64 `json:"measuredMaxMs"`
	MeasuredMedianMs float64 `json:"measuredMedianMs"`
	MarginFactor     float64 `json:"marginFactor"`
}

// defaultPerfMarginFactor is DerivePerfBudget's marginFactor when the
// caller passes zero.
const defaultPerfMarginFactor = 1.5

// TotalCallDurationMs sums the folded logical calls' outer-leg DoneMs. A
// sum, not a median or a max: a run with more calls genuinely did more
// backend work end to end, and that additional work is exactly what a
// perf budget exists to catch.
//
// Folding is not optional here, unlike DiffHops' NoCollapse: core/trace's
// own collapse code documents that a relay's OUTER leg's wall clock
// already CONTAINS the inner one (collapse.go's MergeForDetail:
// "out.T.DoneMs = l.Hop.T.DoneMs // the outer leg's wall clock contains
// the inner"). Summing raw, unfolded legs therefore double-counts every
// relayed call — measured, the same one logical call totals 100ms direct
// versus 205ms through one relay — which would trip a budget on nothing
// but the run's own topology. trace.CollapseRelays is the identity on a
// run with no relays, so folding is universally safe; there is no
// legitimate case for summing raw legs, so unlike DiffHops there is no
// knob to turn it off.
func TotalCallDurationMs(hops []trace.Hop) float64 {
	var total float64
	for _, lh := range trace.CollapseRelays(hops, true) {
		total += lh.Hop.T.DoneMs
	}
	return total
}

// DerivePerfBudget turns a set of observed TotalCallDurationMs samples
// into a budget: the observed max times marginFactor (defaulted to 1.5
// when zero). Rejects an empty sample — there is no max of nothing, and a
// budget silently derived as 0 would fail every future run.
func DerivePerfBudget(samples []float64, marginFactor float64) (PerfBudget, error) {
	if len(samples) == 0 {
		return PerfBudget{}, fmt.Errorf("diff: DerivePerfBudget: samples is empty")
	}
	if marginFactor == 0 {
		marginFactor = defaultPerfMarginFactor
	}

	sorted := append([]float64(nil), samples...)
	sort.Float64s(sorted)
	max := sorted[len(sorted)-1]

	return PerfBudget{
		BudgetMs:         max * marginFactor,
		SampleCount:      len(samples),
		MeasuredMaxMs:    max,
		MeasuredMedianMs: medianOfSorted(sorted),
		MarginFactor:     marginFactor,
	}, nil
}

func medianOfSorted(sorted []float64) float64 {
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

// CheckPerfBudget measures hops with TotalCallDurationMs and compares
// against budgetMs. A zero budget means no budget was configured for this
// flow: the plane reports "unset", not "ok" — "ok" would tell a reviewer
// this flow's perf was checked and passed, when nothing was checked at
// all.
func CheckPerfBudget(hops []trace.Hop, budgetMs float64) PerfResult {
	measured := TotalCallDurationMs(hops)
	if budgetMs == 0 {
		return PerfResult{Status: "unset", MeasuredMs: measured, BudgetMs: budgetMs}
	}
	status := "ok"
	if measured > budgetMs {
		status = "over"
	}
	return PerfResult{Status: status, MeasuredMs: measured, BudgetMs: budgetMs}
}
