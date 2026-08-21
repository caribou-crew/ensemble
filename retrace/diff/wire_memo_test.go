package diff

import (
	"fmt"
	"testing"

	"github.com/caribou-crew/ensemble/core/trace"
)

// TestPairSimilarityMatchesCallSimilarity pins that align's memoized scoring
// path (hopSim/prepHopSim/pairSimilarity) never diverges from the public
// CallSimilarity — i.e., memoizing changed HOW MANY TIMES a pair is scored
// (see TestAlignScoresEachPairAtMostOnce), never WHAT it scores. If this
// test passes for every pair below, align's precomputed sim[][] matrix
// contains exactly what direct CallSimilarity(as[i], bs[j]) calls would
// have, so no pairing outcome can change.
func TestPairSimilarityMatchesCallSimilarity(t *testing.T) {
	hops := []trace.Hop{
		hop(1, "GET", "/x", 200, "", "hello"),
		hop(2, "GET", "/x", 304, "", ""),
		hop(3, "POST", "/x", 200, `{"a":1,"b":2}`, `{"b":2,"a":1}`),
		hop(4, "POST", "/x", 500, `{"a":1,"b":3}`, "goodbye moon zz"),
		hop(5, "GET", "/y", 200, "aaaa", "bbbb"),
		hop(6, "GET", "/y", 404, "zzzz", "yyyy"),
		hop(7, "GET", "/y", 200, "x", "x"),
		hop(8, "GET", "/y", 200, "", ""),
	}
	for _, a := range hops {
		for _, b := range hops {
			want := CallSimilarity(a, b)
			got := pairSimilarity(prepHopSim(a), prepHopSim(b))
			if got != want {
				t.Fatalf("pairSimilarity(prepHopSim(seq=%d), prepHopSim(seq=%d)) = %v, want CallSimilarity = %v",
					a.Seq, b.Seq, got, want)
			}
		}
	}
}

// TestAlignScoresEachPairAtMostOnce pins F12's memoization: align must
// score each (A-index, B-index) pair exactly once — precomputed into
// sim[][] before the DP loop — rather than once in the fill loop and again
// in the traceback loop (the pre-fix behavior). pairSimilarityHook is a
// test-only counting seam incremented on every pairSimilarity call.
//
// Mutation transcript (fix round 2): with align's `sim` matrix removed and
// pairSimilarity(aSim[i], bSim[j]) called directly at both the fill loop's
// and the traceback loop's `diag := ...` sites (i.e. "the cache" reverted
// away, matching the pre-memoization call shape one-for-one), this test's
// `calls != want` check goes RED — calls exceeds n*m because the
// traceback loop scores some pairs a second time. Reverting to the sim[][]
// precomputation restores calls == n*m and the test goes GREEN again.
func TestAlignScoresEachPairAtMostOnce(t *testing.T) {
	n, m := 8, 6
	as := make([]trace.Hop, n)
	for i := range as {
		as[i] = hop(uint64(i), "GET", "/x", 200, "", fmt.Sprintf("resp-a-%d", i))
	}
	bs := make([]trace.Hop, m)
	for j := range bs {
		bs[j] = hop(uint64(j), "GET", "/x", 200, "", fmt.Sprintf("resp-b-%d", j))
	}

	var calls int
	prevHook := pairSimilarityHook
	pairSimilarityHook = func() { calls++ }
	defer func() { pairSimilarityHook = prevHook }()

	align(as, bs)

	want := n * m
	if calls != want {
		t.Fatalf("pairSimilarity called %d times for a %dx%d grid, want exactly %d (every pair scored once, not twice)", calls, n, m, want)
	}
}
