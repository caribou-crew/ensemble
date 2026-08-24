// Package proxy is ensemble's interceptor: one process, N listeners, each
// fronting a real service — capturing hops, propagating trace context, and
// (in later tasks) injecting latency and partitioning recording sessions.
package proxy

import (
	"sync"

	"github.com/caribou-crew/ensemble/core/trace"
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
	subs     map[chan trace.Hop]*subState
	owners   map[string]string // span id -> service name that opened it
	ownerQ   []string          // FIFO for owner eviction
	ownerCap int
	// traceSes maps trace id -> session id, claimed at request START so
	// causal order holds even though hops are recorded at completion
	// (nested calls complete inner-first).
	traceSes map[string]string
	traceQ   []string
	traceCap int
}

// subState tracks one subscriber's replay cursor and how many hops the
// non-blocking fan-out in Record has had to drop for it (buffer full — a
// slow consumer). The counter is read-only from the subscriber's side via
// the Dropped func Subscribe returns.
type subState struct {
	delivered uint64
	dropped   uint64
}

func NewRecorder(opts RecorderOpts) *Recorder {
	if opts.Ring <= 0 {
		opts.Ring = 1024
	}
	return &Recorder{
		opts:     opts,
		subs:     map[chan trace.Hop]*subState{},
		owners:   map[string]string{},
		ownerCap: 65536,
		traceSes: map[string]string{},
		traceCap: 65536,
	}
}

// Record assigns the next sequence number, scrubs, retains, and fans out.
// Schema is stamped here — the single place, so the ring, every subscriber
// (API/SSE), and the NDJSON file all agree; Writer.Write stamps it too but
// only as a no-op safety net for callers that bypass Record.
func (r *Recorder) Record(h trace.Hop) trace.Hop {
	h.Schema = trace.SchemaVersion
	r.mu.Lock()
	redactor := r.opts.Redactor
	r.mu.Unlock()
	if redactor != nil {
		h = redactor.Hop(h)
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
	for ch, st := range r.subs {
		if h.Seq <= st.delivered {
			continue
		}
		select {
		case ch <- h:
			st.delivered = h.Seq
		default:
			// Slow subscriber: drop rather than block the capture path.
			// Counted so a subscriber (e.g. SessionManager) can detect and
			// surface the loss instead of silently under-reporting.
			st.dropped++
		}
	}
	r.mu.Unlock()
	return h
}

// SetRedactor swaps the redaction rules applied to every hop from this call
// onward — e.g. after `ensemble up` reconciles a config whose redact list
// changed. Already-recorded hops are not retroactively scrubbed.
func (r *Recorder) SetRedactor(redactor *trace.Redactor) {
	r.mu.Lock()
	r.opts.Redactor = redactor
	r.mu.Unlock()
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
// The returned dropped func reports how many hops Record has had to drop
// for this subscription because its buffer was full (a slow consumer) —
// callers that need to know their capture may be incomplete (e.g.
// SessionManager, for verdict purposes) should poll it. The returned
// cancel must be called to release the subscription.
func (r *Recorder) Subscribe(cursor uint64) (ch <-chan trace.Hop, dropped func() uint64, cancel func()) {
	r.mu.Lock()
	// Buffer must hold the full replay: it is filled under the lock, so a
	// reader cannot drain it concurrently and a tight buffer would deadlock.
	c := make(chan trace.Hop, len(r.ring)+256)
	st := &subState{delivered: cursor}
	for _, h := range r.ring {
		if h.Seq > cursor {
			c <- h
			st.delivered = h.Seq
		}
	}
	r.subs[c] = st
	r.mu.Unlock()

	dropped = func() uint64 {
		r.mu.Lock()
		defer r.mu.Unlock()
		return st.dropped
	}
	cancel = func() {
		r.mu.Lock()
		if _, ok := r.subs[c]; ok {
			delete(r.subs, c)
			close(c)
		}
		r.mu.Unlock()
	}
	return c, dropped, cancel
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

// ClaimTrace records which session a trace belongs to, at request start.
func (r *Recorder) ClaimTrace(traceID, session string) {
	if traceID == "" || session == "" {
		return
	}
	r.mu.Lock()
	if _, exists := r.traceSes[traceID]; !exists {
		r.traceQ = append(r.traceQ, traceID)
		if len(r.traceQ) > r.traceCap {
			evict := r.traceQ[0]
			r.traceQ = r.traceQ[1:]
			delete(r.traceSes, evict)
		}
	}
	r.traceSes[traceID] = session
	r.mu.Unlock()
}

// TraceSession resolves a trace id to the session that owns it ("" if none).
func (r *Recorder) TraceSession(traceID string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.traceSes[traceID]
}
