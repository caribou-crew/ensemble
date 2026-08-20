package trace

import "strings"

// LogicalHop is one logical exchange: the client's request and the origin's
// response, with pass-through relays folded out. Ported from the local-stack
// prototype (web/src/trace/collapse.ts).
type LogicalHop struct {
	// Hop is the first leg. Its request side represents the exchange and it
	// is the identity used for selection, so it points into the input slice.
	Hop *Hop
	// Origin is the last leg, whose response side and status are the real
	// outcome. Equals Hop when nothing folded.
	Origin *Hop
	// Via lists relay services removed from the middle, in order.
	Via []string
	// Index of Hop in the input slice — callers select by it.
	Index int
	// StatusMismatch: a relay reported a different status than the leg
	// behind it. Such a pair is never folded — a disagreeing relay changed
	// the outcome, which is the case worth seeing.
	StatusMismatch bool
}

// keyOf keys chain candidates. The query string is deliberately excluded:
// producers disagree about where it goes — the leg INTO a relay records a
// bare path while the leg OUT of it folds the query into the path. Trace,
// method and route must still match; the status check is what actually
// establishes that the relay was transparent.
func keyOf(h *Hop) string {
	bare, _, _ := strings.Cut(h.Path, "?")
	return h.TraceID + " " + h.Method + " " + bare
}

// CollapseRelays folds pass-through relays out of a hop list:
// A -> R -> B becomes A -> B.
//
// Detection is structural rather than a list of known relay names — a pair
// only folds when both legs share a traceId, method and route, and the first
// leg's `to` is the second's `from`. That covers any transparent proxy
// without a config edit, and it is provable from the captured data instead
// of assumed.
func CollapseRelays(hops []Hop, enabled bool) []LogicalHop {
	if !enabled {
		out := make([]LogicalHop, len(hops))
		for i := range hops {
			out[i] = LogicalHop{Hop: &hops[i], Origin: &hops[i], Index: i}
		}
		return out
	}

	// Successor lookup: hops that could continue a chain, by (trace, method, route).
	byKey := make(map[string][]int, len(hops))
	for i := range hops {
		if hops[i].TraceID == "" {
			continue // without a trace id two legs can't be proven to be one exchange
		}
		k := keyOf(&hops[i])
		byKey[k] = append(byKey[k], i)
	}

	consumed := make([]bool, len(hops))
	var out []LogicalHop

	for i := range hops {
		if consumed[i] {
			continue
		}
		first := &hops[i]
		last := first
		var via []string
		mismatch := false

		if first.TraceID != "" {
			for {
				next := -1
				for _, j := range byKey[keyOf(last)] {
					// A chain only extends forward, so a cyclic A->B->A
					// can't consume itself twice.
					if j > i && !consumed[j] && hops[j].From == last.To {
						next = j
						break
					}
				}
				if next == -1 {
					break
				}
				if hops[next].Status != last.Status {
					// Leave both legs standing — the relay altered the outcome.
					mismatch = true
					break
				}
				consumed[next] = true
				via = append(via, last.To)
				last = &hops[next]
			}
		}

		consumed[i] = true
		out = append(out, LogicalHop{Hop: first, Origin: last, Via: via, Index: i, StatusMismatch: mismatch})
	}

	return out
}

// MergeForDetail builds the hop to show in a detail pane for a folded
// exchange: the origin's response side with the first leg's request side and
// wall-clock duration grafted on. Returns the hop unchanged when nothing
// folded. (In practice the inbound leg captures the request headers and the
// outbound leg the response headers, so neither row alone is complete.)
func MergeForDetail(l LogicalHop) Hop {
	if len(l.Via) == 0 {
		return *l.Hop
	}
	out := *l.Origin
	out.From = l.Hop.From
	out.T.Start = l.Hop.T.Start
	out.T.DoneMs = l.Hop.T.DoneMs // the outer leg's wall clock contains the inner
	if l.Hop.Req.Headers != nil || l.Hop.Req.Body != "" {
		out.Req = l.Hop.Req
	}
	return out
}
