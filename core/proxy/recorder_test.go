package proxy

import (
	"testing"
	"time"

	"github.com/ensemble-dev/ensemble/core/trace"
)

func TestRecorderAssignsSeqAndSnapshots(t *testing.T) {
	rec := NewRecorder(RecorderOpts{Ring: 8})
	rec.Record(trace.Hop{To: "a"})
	rec.Record(trace.Hop{To: "b"})
	hops := rec.Snapshot()
	if len(hops) != 2 || hops[0].Seq != 1 || hops[1].Seq != 2 {
		t.Fatalf("snapshot: %+v", hops)
	}
}

func TestRecorderRingEvictsOldest(t *testing.T) {
	rec := NewRecorder(RecorderOpts{Ring: 4})
	for i := 0; i < 6; i++ {
		rec.Record(trace.Hop{To: "x"})
	}
	hops := rec.Snapshot()
	if len(hops) != 4 || hops[0].Seq != 3 || hops[3].Seq != 6 {
		t.Fatalf("ring eviction wrong: seqs %d..%d len %d", hops[0].Seq, hops[len(hops)-1].Seq, len(hops))
	}
}

func TestSubscribeReplaysFromCursorThenLive(t *testing.T) {
	rec := NewRecorder(RecorderOpts{Ring: 16})
	rec.Record(trace.Hop{To: "old-1"})
	rec.Record(trace.Hop{To: "old-2"})

	ch, cancel := rec.Subscribe(0)
	defer cancel()

	got := func() trace.Hop {
		select {
		case h := <-ch:
			return h
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for hop")
			return trace.Hop{}
		}
	}

	if h := got(); h.To != "old-1" {
		t.Fatalf("replay order: %+v", h)
	}
	if h := got(); h.To != "old-2" {
		t.Fatalf("replay order: %+v", h)
	}

	rec.Record(trace.Hop{To: "live-1"})
	if h := got(); h.To != "live-1" {
		t.Fatalf("live delivery: %+v", h)
	}
}

func TestSubscribeCursorSkipsAlreadySeen(t *testing.T) {
	rec := NewRecorder(RecorderOpts{Ring: 16})
	rec.Record(trace.Hop{To: "a"}) // seq 1
	rec.Record(trace.Hop{To: "b"}) // seq 2
	ch, cancel := rec.Subscribe(1) // cursor: last seen seq 1
	defer cancel()
	select {
	case h := <-ch:
		if h.Seq != 2 {
			t.Fatalf("want seq 2 first, got %d", h.Seq)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout")
	}
}

func TestSubscribeReplaysFullRingWithoutDeadlock(t *testing.T) {
	rec := NewRecorder(RecorderOpts{Ring: 2048})
	for i := 0; i < 2048; i++ {
		rec.Record(trace.Hop{To: "x"})
	}
	done := make(chan int)
	go func() {
		ch, cancel := rec.Subscribe(0)
		defer cancel()
		n := 0
		for range 2048 {
			<-ch
			n++
		}
		done <- n
	}()
	select {
	case n := <-done:
		if n != 2048 {
			t.Fatalf("replayed %d of 2048", n)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("subscribe deadlocked replaying a ring larger than the channel buffer")
	}
}

func TestRecorderAppliesRedaction(t *testing.T) {
	rec := NewRecorder(RecorderOpts{Ring: 8, Redactor: trace.NewRedactor(nil, 0)})
	rec.Record(trace.Hop{To: "a", Req: trace.Payload{Headers: map[string]string{"authorization": "Bearer x"}}})
	if got := rec.Snapshot()[0].Req.Headers["authorization"]; got != trace.Redacted {
		t.Fatalf("not redacted: %q", got)
	}
}
