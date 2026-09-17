package main

import (
	"testing"

	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/retrace/diff"
)

func replayRequestHop(seq uint64, body string) trace.Hop {
	return trace.Hop{Seq: seq, Method: "POST", Path: "/api/v2/user/risk/login-evaluation", Req: trace.Payload{Body: body}}
}

func TestPairReplayRequestsUsesMatchedExchangeIdentityBeforeArrivalOrder(t *testing.T) {
	reference := []trace.Hop{
		replayRequestHop(41, `{"password":"secret"}`),
		replayRequestHop(42, `{"passcode":"123456"}`),
	}
	// ObservedHops keeps arrival order, but Seq is the recorded exchange
	// selected by replay.Match. Concurrent calls may therefore arrive 42,41.
	observed := []trace.Hop{
		replayRequestHop(42, `{"passcode":"123456"}`),
		replayRequestHop(41, `{"password":"secret"}`),
	}

	w := diff.DiffWire(reference, orderObservedByMatchedExchange(reference, observed), diff.Options{})
	if len(w.Paired) != 2 || len(w.Missing) != 0 || len(w.Extra) != 0 {
		t.Fatalf("wire counts = paired %d, missing %d, extra %d; want 2,0,0", len(w.Paired), len(w.Missing), len(w.Extra))
	}
	for _, entry := range w.Paired {
		if len(entry.BodyDiff) != 0 {
			t.Fatalf("matched exchange %d reported request changes: %+v", entry.SeqA, entry.BodyDiff)
		}
	}
}

func TestPairReplayRequestsDoesNotHideChangedValuesOrCountDrift(t *testing.T) {
	reference := []trace.Hop{
		replayRequestHop(41, `{"amount":100}`),
		replayRequestHop(42, `{"amount":200}`),
	}

	t.Run("changed", func(t *testing.T) {
		observed := []trace.Hop{
			replayRequestHop(42, `{"amount":201}`),
			replayRequestHop(41, `{"amount":100}`),
		}
		w := diff.DiffWire(reference, orderObservedByMatchedExchange(reference, observed), diff.Options{})
		if len(w.Paired) != 2 || len(w.Paired[1].BodyDiff) != 1 {
			t.Fatalf("changed wire = %+v, want one visible body change on matched exchange 42", w)
		}
	})

	for _, tc := range []struct {
		name     string
		observed []trace.Hop
		missing  int
		extra    int
	}{
		{name: "missing", observed: []trace.Hop{replayRequestHop(41, `{"amount":100}`)}, missing: 1},
		{name: "extra duplicate", observed: []trace.Hop{replayRequestHop(41, `{"amount":100}`), replayRequestHop(42, `{"amount":200}`), replayRequestHop(42, `{"amount":200}`)}, extra: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := diff.DiffWire(reference, orderObservedByMatchedExchange(reference, tc.observed), diff.Options{})
			if len(w.Missing) != tc.missing || len(w.Extra) != tc.extra {
				t.Fatalf("wire counts = missing %d, extra %d; want %d,%d", len(w.Missing), len(w.Extra), tc.missing, tc.extra)
			}
		})
	}
}
