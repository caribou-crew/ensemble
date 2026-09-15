package trace

import (
	"encoding/json"
	"os"
	"testing"
)

func TestResolveClientOwnIdentityWins(t *testing.T) {
	h := Hop{TraceID: "t1", Client: "app-legacy"}
	idx := ClientIndex([]Hop{h})
	got, ok := ResolveClient(h, idx)
	if !ok || got != "app-legacy" {
		t.Fatalf("ResolveClient = (%q, %v), want (%q, true)", got, ok, "app-legacy")
	}
}

func TestResolveClientInheritsThroughTrace(t *testing.T) {
	entry := Hop{Seq: 1, TraceID: "t1", Client: "app-legacy"}
	inner := Hop{Seq: 2, TraceID: "t1"}
	idx := ClientIndex([]Hop{entry, inner})
	got, ok := ResolveClient(inner, idx)
	if !ok || got != "app-legacy" {
		t.Fatalf("downstream hop = (%q, %v), want (%q, true) — a hop with no client of its own must inherit its trace's", got, ok, "app-legacy")
	}
}

func TestResolveClientUnattributedWhenTraceHasNone(t *testing.T) {
	inner := Hop{Seq: 2, TraceID: "t1"}
	idx := ClientIndex([]Hop{inner})
	if got, ok := ResolveClient(inner, idx); ok {
		t.Fatalf("ResolveClient = (%q, true), want unattributed — no hop in the trace claimed a client", got)
	}
}

// A hop with no traceId must consult its own field and nothing else. The
// window here deliberately contains a client-carrying hop, so an
// implementation that falls back to "any client in the window" fails.
func TestResolveClientNoTraceIDJoinsNothing(t *testing.T) {
	loose := Hop{Seq: 2}
	idx := ClientIndex([]Hop{{Seq: 1, TraceID: "t1", Client: "app-legacy"}, loose})
	if got, ok := ResolveClient(loose, idx); ok {
		t.Fatalf("hop with no traceId = (%q, true), want unattributed", got)
	}

	own := Hop{Seq: 3, Client: "app-next"}
	if got, ok := ResolveClient(own, ClientIndex([]Hop{own})); !ok || got != "app-next" {
		t.Fatalf("hop with no traceId but its own client = (%q, %v), want (%q, true)", got, ok, "app-next")
	}
}

// The conflict rule must not depend on which disagreeing hop was seen
// first, so the same window is asserted in BOTH orderings. A first-wins or
// last-wins implementation passes one ordering and fails the other; a
// single-ordering test could not fail for the right reason here.
func TestResolveClientConflictingTraceIsOrderIndependent(t *testing.T) {
	legacy := Hop{Seq: 1, TraceID: "t4", Client: "app-legacy"}
	next := Hop{Seq: 2, TraceID: "t4", Client: "app-next"}
	silent := Hop{Seq: 3, TraceID: "t4"}

	for _, tc := range []struct {
		name   string
		window []Hop
	}{
		{"legacy first", []Hop{legacy, next, silent}},
		{"next first", []Hop{next, legacy, silent}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			idx := ClientIndex(tc.window)
			for _, h := range tc.window {
				if got, ok := ResolveClient(h, idx); ok {
					t.Errorf("seq %d = (%q, true), want unattributed — every hop of a conflicting trace is unattributed", h.Seq, got)
				}
			}
		})
	}
}

func TestMatchesClient(t *testing.T) {
	entry := Hop{Seq: 1, TraceID: "t1", Client: "app-legacy"}
	inner := Hop{Seq: 2, TraceID: "t1"}
	orphan := Hop{Seq: 3, TraceID: "t9"}
	idx := ClientIndex([]Hop{entry, inner, orphan})

	for _, tc := range []struct {
		name string
		hop  Hop
		want string
		ok   bool
	}{
		{"empty want matches everything", orphan, "", true},
		{"entry hop matches its client", entry, "app-legacy", true},
		{"downstream hop matches its client", inner, "app-legacy", true},
		{"another client does not match", entry, "app-next", false},
		{"unattributed sentinel selects the orphan", orphan, UnattributedClient, true},
		{"unattributed sentinel excludes an attributed hop", inner, UnattributedClient, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := MatchesClient(tc.hop, idx, tc.want); got != tc.ok {
				t.Fatalf("MatchesClient(seq %d, %q) = %v, want %v", tc.hop.Seq, tc.want, got, tc.ok)
			}
		})
	}
}

// The sentinel must be unrepresentable as a client identity. core/proxy
// owns ValidClient and imports this package, so the charset cannot be
// asserted from here — core/proxy's own test does that. What CAN be pinned
// here is the property that makes it work: the sentinel does not start with
// the [a-z0-9] a valid identity must begin with.
func TestUnattributedSentinelCannotBeAnIdentity(t *testing.T) {
	c := UnattributedClient[0]
	if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
		t.Fatalf("UnattributedClient = %q starts with %q, which a valid client identity may also start with — the sentinel could collide with a real client", UnattributedClient, string(c))
	}
}

// The cross-language contract. dashboard/ensemble-ui asserts the same file;
// see its clientAttribution test.
func TestClientAttributionFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/client_attribution.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Window []Hop `json:"window"`
		Expect []struct {
			Seq        uint64 `json:"seq"`
			Client     string `json:"client"`
			Attributed bool   `json:"attributed"`
			Why        string `json:"why"`
		} `json:"expect"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Window) != len(fixture.Expect) {
		t.Fatalf("fixture has %d hops and %d expectations — every hop must have one", len(fixture.Window), len(fixture.Expect))
	}

	bySeq := make(map[uint64]Hop, len(fixture.Window))
	for _, h := range fixture.Window {
		bySeq[h.Seq] = h
	}
	idx := ClientIndex(fixture.Window)
	for _, want := range fixture.Expect {
		h, ok := bySeq[want.Seq]
		if !ok {
			t.Fatalf("fixture expects seq %d, which is not in the window", want.Seq)
		}
		got, attributed := ResolveClient(h, idx)
		if attributed != want.Attributed || got != want.Client {
			t.Errorf("seq %d = (%q, %v), want (%q, %v) — %s",
				want.Seq, got, attributed, want.Client, want.Attributed, want.Why)
		}
	}
}
