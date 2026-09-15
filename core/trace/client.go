package trace

// UnattributedClient is the filter value that selects hops belonging to no
// client at all.
//
// Deliberately unrepresentable as a real client identity: core/proxy's
// ValidClient requires a leading [a-z0-9], so no application can ever be
// called "(none)" and collide with the sentinel. A friendlier spelling —
// "none", "unknown" — would be a legal identity, and the day someone named
// a client that, their traffic would silently become the unattributed
// bucket.
const UnattributedClient = "(none)"

// conflictedClient is the index value for a trace whose hops disagree about
// which client started it. "" cannot be a real entry (ClientIndex only ever
// inserts non-empty identities), so it is free to mean "resolved, and the
// answer is that there is no single answer" — distinct from a key being
// absent, which means no hop in the trace claimed a client at all. Both
// resolve to unattributed; keeping them distinct is what makes the conflict
// rule order-independent.
const conflictedClient = ""

// ClientIndex maps each trace id in hops to the client identity that trace
// belongs to, for ResolveClient to read.
//
// A trace whose hops carry two or more DIFFERENT client identities maps to
// a conflict, which resolves to unattributed rather than to whichever hop
// happened to be seen first. That case should not arise — one chain has one
// origin — but a stack that forwards client-identity headers inconsistently
// can produce it, and first-wins would then put hops in a pane where they
// do not belong. A view whose whole purpose is "these calls came from THIS
// app" cannot survive being confidently wrong about that; reporting the
// chain as unattributed shows the developer a fact about their stack
// instead.
func ClientIndex(hops []Hop) map[string]string {
	idx := make(map[string]string)
	for _, h := range hops {
		ObserveClient(idx, h)
	}
	return idx
}

// ObserveClient folds one hop into an index, for a consumer that sees hops
// arriving over time rather than as a slice — a live stream, or a single
// forward pass over a hops file. Same rules as ClientIndex, which is
// defined in terms of it so the two cannot disagree about a conflict.
//
// Order-independent by construction: once a trace is marked conflicted no
// later hop un-marks it, so folding a window in any order yields the same
// index.
func ObserveClient(idx map[string]string, h Hop) {
	if h.TraceID == "" || h.Client == "" {
		return
	}
	prev, seen := idx[h.TraceID]
	if !seen {
		idx[h.TraceID] = h.Client
		return
	}
	if prev != conflictedClient && prev != h.Client {
		idx[h.TraceID] = conflictedClient
	}
}

// ResolveClient reports which client a hop belongs to, and whether it
// belongs to one at all.
//
// A hop's own Client wins when its trace agrees; a hop with none inherits
// the client of the hop that shares its trace id. This fallback matters
// because a client identity is only as propagated as the stack makes it:
// core/proxy carries it in baggage (BaggageClient) so a chain that
// forwards trace context reports it on every hop, but a service that drops
// baggage while keeping traceparent leaves its downstream hops with no
// client of their own. Resolving through the trace recovers exactly those
// hops — and they are the interesting ones, since a routing difference
// between two apps lives in the fan-out, not in the entry call.
//
// Never written back to a Hop. Hop.Client is what the stack actually said;
// this is an inference drawn over a window of hops, and a window that does
// not contain the trace's client-carrying hop legitimately resolves the
// same hop differently. Persisting it would make the two indistinguishable.
//
// idx may be nil, which simply means nothing inherits.
func ResolveClient(h Hop, idx map[string]string) (string, bool) {
	if h.TraceID != "" {
		if c, ok := idx[h.TraceID]; ok {
			if c == conflictedClient {
				return "", false
			}
			return c, true
		}
		// The trace is in no index entry, so no hop in the window claimed a
		// client for it — including this one. Fall through rather than
		// return early: a nil idx must still resolve a lone hop's own field.
	}
	if h.Client != "" {
		return h.Client, true
	}
	return "", false
}

// MatchesClient reports whether a hop belongs to the client a filter asked
// for. want is a client identity, or UnattributedClient to select the hops
// that belong to none. An empty want matches everything — "no filter".
func MatchesClient(h Hop, idx map[string]string, want string) bool {
	if want == "" {
		return true
	}
	got, ok := ResolveClient(h, idx)
	if want == UnattributedClient {
		return !ok
	}
	return ok && got == want
}
