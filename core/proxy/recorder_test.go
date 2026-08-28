package proxy

import (
	"testing"
	"time"

	"github.com/caribou-crew/ensemble/core/trace"
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

// TestRecordStampsSchemaVersion guards final-review finding I3:
// Writer.Write previously stamped Hop.Schema only on its own local copy, so
// a hop served straight from the recorder (ring/API) carried an empty
// schema while the byte-identical hop on disk carried trace.SchemaVersion.
// Record must stamp it once, in one place, so every consumer agrees.
func TestRecordStampsSchemaVersion(t *testing.T) {
	rec := NewRecorder(RecorderOpts{Ring: 8})
	got := rec.Record(trace.Hop{To: "a"})
	if got.Schema != trace.SchemaVersion {
		t.Fatalf("Record returned Schema = %q, want %q", got.Schema, trace.SchemaVersion)
	}
	snap := rec.Snapshot()
	if len(snap) != 1 || snap[0].Schema != trace.SchemaVersion {
		t.Fatalf("Snapshot()[0].Schema = %q, want %q", snap[0].Schema, trace.SchemaVersion)
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

	ch, _, cancel := rec.Subscribe(0)
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
	rec.Record(trace.Hop{To: "a"})    // seq 1
	rec.Record(trace.Hop{To: "b"})    // seq 2
	ch, _, cancel := rec.Subscribe(1) // cursor: last seen seq 1
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

// TestSubscribeDroppedCountsOverflow guards final-review finding I8: the
// Recorder's fan-out to a subscriber is non-blocking (Record must never
// stall the capture path on a slow consumer), so a full buffer silently
// dropped hops with nothing tracking it. dropped() must report how many
// hops were lost for THIS subscription once its buffer fills, so a
// consumer (e.g. SessionManager) can detect that its capture is
// incomplete instead of silently under-reporting.
func TestSubscribeDroppedCountsOverflow(t *testing.T) {
	// Subscribe's buffer is sized len(ring)+256 at subscription time; the
	// ring is empty here (nothing Recorded yet), so the buffer cap is 256.
	rec := NewRecorder(RecorderOpts{Ring: 8})
	ch, dropped, cancel := rec.Subscribe(0)
	defer cancel()
	_ = ch // deliberately never drained: the slow-subscriber scenario

	const sent = 1000
	for i := 0; i < sent; i++ {
		rec.Record(trace.Hop{To: "x"})
	}

	const bufCap = 256
	wantDropped := uint64(sent - bufCap)
	if got := dropped(); got != wantDropped {
		t.Fatalf("dropped() = %d, want %d (sent %d into a %d-cap buffer)", got, wantDropped, sent, bufCap)
	}
}

func TestSubscribeReplaysFullRingWithoutDeadlock(t *testing.T) {
	rec := NewRecorder(RecorderOpts{Ring: 2048})
	for i := 0; i < 2048; i++ {
		rec.Record(trace.Hop{To: "x"})
	}
	done := make(chan int)
	go func() {
		ch, _, cancel := rec.Subscribe(0)
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
	rec := NewRecorder(RecorderOpts{Ring: 8, Redactor: mustRedactor(t, nil, 0)})
	rec.Record(trace.Hop{To: "a", Req: trace.Payload{Headers: map[string]string{"authorization": "Bearer x"}}})
	if got := rec.Snapshot()[0].Req.Headers["authorization"]; got != trace.Redacted {
		t.Fatalf("not redacted: %q", got)
	}
}
