package proxy

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caribou-crew/ensemble/core/trace"
)

// gateWriter blocks every Write until released — a disk that has stalled
// completely, the case the bounded write queue exists for.
type gateWriter struct {
	release chan struct{}
	mu      sync.Mutex
	writes  int
}

func (g *gateWriter) Write(b []byte) (int, error) {
	<-g.release
	g.mu.Lock()
	g.writes++
	g.mu.Unlock()
	return len(b), nil
}

// TestRecordLatencyUnaffectedByStalledWriter is the 7.2 contract: with the
// writer goroutine wedged on a Write that never returns, Record must stay
// non-blocking — overflow past the queue is dropped and counted, never
// waited for.
func TestRecordLatencyUnaffectedByStalledWriter(t *testing.T) {
	gate := &gateWriter{release: make(chan struct{})}
	rec := NewRecorder(RecorderOpts{Ring: 8, Writer: trace.NewWriter(gate)})

	// One hop enters the writer goroutine and wedges; writeQueueCap more
	// fill the queue; everything beyond that must drop, not block.
	const total = writeQueueCap + 200
	start := time.Now()
	for i := 0; i < total; i++ {
		rec.Record(trace.Hop{To: "x"})
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("recording %d hops against a stalled writer took %v — Record is blocking on the disk", total, elapsed)
	}

	// The writer took one hop off the queue before wedging, so the drop
	// count can be off by one from the naive arithmetic — assert the floor.
	if d := rec.DroppedWrites(); d < 100 {
		t.Fatalf("DroppedWrites = %d, want the overflow counted (>= 100)", d)
	}
	if e := rec.WriteErrors(); e != 0 {
		t.Fatalf("WriteErrors = %d, want 0 — a stall is a drop, not a write failure", e)
	}

	// Unwedge and Close: the flush drains what was queued.
	close(gate.release)
	rec.Close()
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if gate.writes == 0 {
		t.Fatal("Close flushed nothing — queued hops were lost")
	}
}

// errWriter fails every write — a disk gone read-only mid-run.
type errWriter struct{}

func (errWriter) Write([]byte) (int, error) { return 0, &writeFailure{} }

type writeFailure struct{}

func (*writeFailure) Error() string { return "disk gone" }

func TestWriteErrorsCountedNotSwallowed(t *testing.T) {
	rec := NewRecorder(RecorderOpts{Ring: 8, Writer: trace.NewWriter(errWriter{})})
	rec.Record(trace.Hop{To: "a"})
	rec.Record(trace.Hop{To: "b"})
	rec.Close() // flush guarantees both writes were attempted
	if e := rec.WriteErrors(); e != 2 {
		t.Fatalf("WriteErrors = %d, want 2", e)
	}
}

// TestRingByteBudgetEvictsOldest: the count cap alone is not a memory
// bound; a small byte budget must evict old hops long before the count cap
// is reached, and a single over-budget hop still stays.
func TestRingByteBudgetEvictsOldest(t *testing.T) {
	rec := NewRecorder(RecorderOpts{Ring: 100, RingBytes: 8 << 10})
	body := strings.Repeat("x", 1024)
	for i := 0; i < 50; i++ {
		rec.Record(trace.Hop{To: "x", Resp: trace.Payload{Body: body}})
	}
	snap := rec.Snapshot()
	if len(snap) == 0 || len(snap) >= 50 {
		t.Fatalf("ring holds %d hops, want the byte budget to have evicted most of 50", len(snap))
	}
	if snap[len(snap)-1].Seq != 50 {
		t.Fatalf("newest hop = seq %d, want 50 — eviction must trim the FRONT", snap[len(snap)-1].Seq)
	}

	// One hop bigger than the whole budget is kept — the ring never evicts
	// below a single element.
	rec.Record(trace.Hop{To: "big", Resp: trace.Payload{Body: strings.Repeat("y", 16<<10)}})
	snap = rec.Snapshot()
	if len(snap) != 1 || snap[0].To != "big" {
		t.Fatalf("ring = %d hops (last %+v), want exactly the one over-budget hop", len(snap), snap[len(snap)-1].To)
	}
}
