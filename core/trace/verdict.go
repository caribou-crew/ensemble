package trace

// Verdict is a capture-trust rating carried by recordings and sessions:
// how much a consumer should trust that the capture is complete and
// correctly attributed. Concept ported from the JS prototype.
type Verdict string

const (
	VerdictOK       Verdict = "ok"
	VerdictSuspect  Verdict = "suspect"  // heuristic doubt (e.g. unattributed traffic mid-run)
	VerdictDegraded Verdict = "degraded" // proven gap (e.g. a service dropped baggage)
	VerdictBroken   Verdict = "broken"   // capture machinery failed mid-run
	VerdictFailed   Verdict = "failed"   // no usable capture
)

var verdictRank = map[Verdict]int{
	VerdictOK: 0, VerdictSuspect: 1, VerdictDegraded: 2, VerdictBroken: 3, VerdictFailed: 4,
}

// Worse returns the more severe of the two verdicts.
func (v Verdict) Worse(o Verdict) Verdict {
	if verdictRank[o] > verdictRank[v] {
		return o
	}
	return v
}
