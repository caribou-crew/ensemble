package proxy

import (
	"errors"
	"sync"
	"testing"

	"github.com/caribou-crew/ensemble/core/trace"
)

// TestDuplicateSessionIDRefusedBeforeBind: a second Start under a live id
// is a 409-shaped refusal (ErrSessionActive), and racing Starts with the
// same never-before-seen id resolve to exactly one winner — the post-bind
// double-check under the lock, exercised under -race.
func TestDuplicateSessionIDRefusedBeforeBind(t *testing.T) {
	rec := NewRecorder(RecorderOpts{Ring: 16})
	p := New(rec)
	defer p.Close()
	frontProxy := buildChain(t, p, []string{"traceparent", "baggage"})
	mgr := NewSessionManager(p, rec, []string{"svc-front"})
	defer mgr.Close()

	if _, err := mgr.Start("dup", "svc-front", "http://"+frontProxy, "", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Start("dup", "svc-front", "http://"+frontProxy, "", 0); !errors.Is(err, ErrSessionActive) {
		t.Fatalf("second Start err = %v, want ErrSessionActive", err)
	}
	mgr.End("dup")

	const racers = 8
	var wg sync.WaitGroup
	errs := make([]error, racers)
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = mgr.Start("raced", "svc-front", "http://"+frontProxy, "", 0)
		}(i)
	}
	wg.Wait()
	winners := 0
	for i, err := range errs {
		if err == nil {
			winners++
		} else if !errors.Is(err, ErrSessionActive) {
			t.Fatalf("racer %d failed with %v, want ErrSessionActive", i, err)
		}
	}
	if winners != 1 {
		t.Fatalf("%d concurrent Starts won, want exactly 1", winners)
	}
}

// TestHopsRoutedAfterEndCountAsDropped: a fresh hop carrying an ended
// session's id lands on that session's droppedHops counter — never
// vanishes — while the finalization of a hop the session kept while live
// does NOT count (the original was recorded).
func TestHopsRoutedAfterEndCountAsDropped(t *testing.T) {
	rec := NewRecorder(RecorderOpts{Ring: 16})
	p := New(rec)
	defer p.Close()
	frontProxy := buildChain(t, p, []string{"traceparent", "baggage"})
	mgr := NewSessionManager(p, rec, []string{"svc-front"})
	defer mgr.Close()

	ses, err := mgr.Start("run-late", "svc-front", "http://"+frontProxy, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	mustGet(t, "http://"+ses.EdgeAddr+"/before-end")
	waitFor(t, "live hops", func() bool { return len(ses.Hops()) == 3 })

	ended := mgr.End("run-late")
	if ended != ses {
		t.Fatalf("End returned %v, want the session", ended)
	}
	if mgr.Get("run-late") != nil {
		t.Fatal("ended session still routable via Get")
	}

	// A straggler completing after End — recorded straight into the
	// recorder the way an in-flight downstream hop would be.
	rec.Record(trace.Hop{Session: "run-late", To: "svc-leaf", TraceID: "t-straggler"})
	waitFor(t, "dropped count", func() bool { return ses.DroppedHops() == 1 })

	// A finalization (Updated) of a hop the session already holds is not a
	// loss: only fresh hops count.
	kept := ses.Hops()[0]
	rec.Update(kept)
	rec.Record(trace.Hop{Session: "run-late", To: "svc-leaf", TraceID: "t-straggler-2"})
	waitFor(t, "second dropped hop", func() bool { return ses.DroppedHops() == 2 })
	if got := ses.DroppedHops(); got != 2 {
		t.Fatalf("DroppedHops = %d, want 2 — the Updated event must not count", got)
	}
	if len(ses.Hops()) != 3 {
		t.Fatalf("ended session grew hops: %d", len(ses.Hops()))
	}
}

// TestRestartedSessionIDShedsItsEndedPredecessor: Start on an id that
// previously ended must route to the NEW session; stragglers for the old
// run no longer increment the old counter through the ended map.
func TestRestartedSessionIDShedsItsEndedPredecessor(t *testing.T) {
	rec := NewRecorder(RecorderOpts{Ring: 16})
	p := New(rec)
	defer p.Close()
	frontProxy := buildChain(t, p, []string{"traceparent", "baggage"})
	mgr := NewSessionManager(p, rec, []string{"svc-front"})
	defer mgr.Close()

	old, err := mgr.Start("run-r", "svc-front", "http://"+frontProxy, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	mgr.End("run-r")

	fresh, err := mgr.Start("run-r", "svc-front", "http://"+frontProxy, "", 0)
	if err != nil {
		t.Fatalf("restarting an ended id: %v", err)
	}
	rec.Record(trace.Hop{Session: "run-r", To: "svc-leaf", TraceID: "t-restart"})
	waitFor(t, "hop routed to the live session", func() bool { return len(fresh.Hops()) == 1 })
	if old.DroppedHops() != 0 {
		t.Fatalf("old session counted the new run's hop as dropped: %d", old.DroppedHops())
	}
}
