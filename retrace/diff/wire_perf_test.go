package diff

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/caribou-crew/ensemble/core/trace"
)

// shapedHops builds n hops shaped like the F12 review's own reproduction
// case: a ~20KB JSON body per hop, differing enough between every A/B pair
// (distinct id per side, so no two hops share a canonical body) that
// CallSimilarity's dice-bigram fallback runs for the whole grid rather than
// short-circuiting on canonical-form equality — the worst case the review
// measured at 9.5s for 120x120.
func shapedHops(prefix string, n int) []trace.Hop {
	pad := strings.Repeat("lorem-ipsum-dolor-sit-amet-", 700) // ~19KB
	hops := make([]trace.Hop, n)
	for i := range hops {
		body := fmt.Sprintf(`{"id":"%s-%d","payload":"%s"}`, prefix, i, pad)
		hops[i] = hop(uint64(i), "POST", fmt.Sprintf("/orders/%d", i%37), 200, body, body)
	}
	return hops
}

// TestAlignPerformanceOnAShapedInput is the F12 before/after timing case
// (task-8-review.md: "120x120 with 20KB bodies takes 9.5s"). Measured on
// this exact input: fix round 1's align (commit 1584c3a, no A/B hop shares
// a canonical body, so every one of the 14,400 cells forces a full
// ~20KB dice-bigram computation) took 28.03s; fix round 2's memoized align
// (this commit) takes 0.225s on the same input — see task-8-report.md's
// Fix round 2 section for the transcript this number came from.
func TestAlignPerformanceOnAShapedInput(t *testing.T) {
	if testing.Short() {
		t.Skip("shaped-input timing test skipped in -short mode")
	}
	a := shapedHops("a", 120)
	b := shapedHops("b", 120)

	start := time.Now()
	pairs, aOnly, bOnly := align(a, b)
	elapsed := time.Since(start)

	t.Logf("align(120x120, ~20KB bodies) took %s (pairs=%d aOnly=%d bOnly=%d)", elapsed, len(pairs), len(aOnly), len(bOnly))
	// The threshold is derived from the GAP this test must detect, not from
	// measured current performance plus a margin. Memoized: 0.225s without
	// -race, 2.0-3.1s under it. Un-memoized: 28s without -race, and worse
	// under it. Any value between those two populations catches the
	// regression; 10s sits ~3x above the slow end of "fixed" and far below
	// "broken", so it cannot flake on a loaded CI box while still failing
	// loudly if the sim[][] cache is ever lost.
	//
	// A 3s bound here was measured WITHOUT -race and failed 1 run in 4 under
	// it — and -race is what the mandated CI command uses. A perf guard
	// tuned to the fast path is a flaky test wearing a regression test's
	// clothes.
	if elapsed > 10*time.Second {
		t.Fatalf("align(120x120, ~20KB bodies) took %s, want far under the un-memoized ~28s (regression guard: 10s)", elapsed)
	}
}
