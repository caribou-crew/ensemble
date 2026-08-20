// Package proxy is ensemble's interceptor: one process, N listeners, each
// fronting a real service — capturing hops, propagating trace context, and
// (in later tasks) injecting latency and partitioning recording sessions.
package proxy

import (
	"sync"

	"github.com/ensemble-dev/ensemble/core/trace"
)

// RecorderOpts configures hop retention and scrubbing.
type RecorderOpts struct {
	// Ring is how many hops are kept in memory for replay to late
	// subscribers. Zero means a default of 1024.
	Ring int
	// Redactor scrubs hops on the way in. Nil records hops verbatim —
	// only acceptable in tests.
	Redactor *trace.Redactor
	// Writer, when set, also appends every hop as NDJSON.
	Writer *trace.Writer
}

// Recorder is the single sink for captured hops: an in-memory replay ring,
// live fan-out to subscribers, and span-ownership tracking that lets the
// next hop name its caller.
type Recorder struct {
	mu       sync.Mutex
	opts     RecorderOpts
	ring     []trace.Hop
	nextSeq  uint64
	subs     map[chan trace.Hop]uint64 // value: seq already delivered through
	owners   map[string]string         // span id -> service name that opened it
	ownerQ   []string                  // FIFO for owner eviction
	ownerCap int
}

func NewRecorder(opts RecorderOpts) *Recorder {
	if opts.Ring <= 0 {
		opts.Ring = 1024
	}
	return &Recorder{
		opts:     opts,
		subs:     map[chan trace.Hop]uint64{},
		owners:   map[string]string{},
		ownerCap: 65536,
	}
}

// Record assigns the next sequence number, scrubs, retains, and fans out.
func (r *Recorder) Record(h trace.Hop) trace.Hop {
	if r.opts.Redactor != nil {
		h = r.opts.Redactor.Hop(h)
	}
	r.mu.Lock()
	r.nextSeq++
	h.Seq = r.nextSeq
	r.ring = append(r.ring, h)
	if len(r.ring) > r.opts.Ring {
		r.ring = r.ring[len(r.ring)-r.opts.Ring:]
	}
	if r.opts.Writer != nil {
		// Best-effort: a full disk must not take the proxy down with it.
		_ = r.opts.Writer.Write(h)
	}
	for ch, seen := range r.subs {
		if h.Seq <= seen {
			continue
		}
		select {
		case ch <- h:
			r.subs[ch] = h.Seq
		default:
			// Slow subscriber: drop rather than block the capture path.
		}
	}
	r.mu.Unlock()
	return h
}

// Snapshot returns the retained hops, oldest first.
func (r *Recorder) Snapshot() []trace.Hop {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]trace.Hop, len(r.ring))
	copy(out, r.ring)
	return out
}

// Subscribe replays retained hops with Seq > cursor, then delivers live.
// The returned cancel must be called to release the subscription.
func (r *Recorder) Subscribe(cursor uint64) (<-chan trace.Hop, func()) {
	r.mu.Lock()
	// Buffer must hold the full replay: it is filled under the lock, so a
	// reader cannot drain it concurrently and a tight buffer would deadlock.
	ch := make(chan trace.Hop, len(r.ring)+256)
	delivered := cursor
	for _, h := range r.ring {
		if h.Seq > cursor {
			ch <- h
			delivered = h.Seq
		}
	}
	r.subs[ch] = delivered
	r.mu.Unlock()

	cancel := func() {
		r.mu.Lock()
		if _, ok := r.subs[ch]; ok {
			delete(r.subs, ch)
			close(ch)
		}
		r.mu.Unlock()
	}
	return ch, cancel
}

// ClaimSpan registers which service opened a span, so the hop that carries
// it as parent can name its caller.
func (r *Recorder) ClaimSpan(spanID, service string) {
	r.mu.Lock()
	if _, exists := r.owners[spanID]; !exists {
		r.ownerQ = append(r.ownerQ, spanID)
		if len(r.ownerQ) > r.ownerCap {
			evict := r.ownerQ[0]
			r.ownerQ = r.ownerQ[1:]
			delete(r.owners, evict)
		}
	}
	r.owners[spanID] = service
	r.mu.Unlock()
}

// SpanOwner resolves a span id to the service that opened it ("" if unknown
// — a client or an unproxied caller).
func (r *Recorder) SpanOwner(spanID string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.owners[spanID]
}
