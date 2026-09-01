// Package proxy is ensemble's interceptor: one process, N listeners, each
// fronting a real service — capturing hops, propagating trace context, and
// (in later tasks) injecting latency and partitioning recording sessions.
package proxy

import (
	"sort"
	"sync"

	"github.com/caribou-crew/ensemble/core/trace"
)

// HopRedactor scrubs one hop on its way into the recorder. *trace.Redactor
// is the production implementation; a non-nil error means some value could
// not be redacted as configured (the returned hop is already fail-closed —
// see trace.Redactor.Hop) and Record degrades the hop rather than dropping
// it or killing the request.
type HopRedactor interface {
	Hop(trace.Hop) (trace.Hop, error)
}

// DefaultRingBytes is the default byte budget for the in-memory hop ring.
// The count cap alone is not a memory bound: 1024 hops each carrying two
// 256 KB bodies is half a gigabyte resident, so eviction enforces bytes AND
// count together — see Record.
const DefaultRingBytes = 256 << 20

// writeQueueCap bounds the NDJSON write queue. Deep enough that a normal
// disk never falls behind a burst, shallow enough that a stalled disk costs
// megabytes, not the machine — overflow drops the write and counts it (see
// Record), never blocks the request.
const writeQueueCap = 4096

// RecorderOpts configures hop retention and scrubbing.
type RecorderOpts struct {
	// Ring is how many hops are kept in memory for replay to late
	// subscribers. Zero means a default of 1024.
	Ring int
	// RingBytes is the ring's byte budget: eviction removes oldest hops
	// until the ring is under BOTH the count cap and this. Zero means
	// DefaultRingBytes.
	RingBytes int64
	// Redactor scrubs hops on the way in. Nil records hops verbatim —
	// only acceptable in tests.
	Redactor HopRedactor
	// Writer, when set, also appends every hop as NDJSON — asynchronously,
	// through a bounded ordered queue drained by one writer goroutine, so
	// disk latency never serializes requests. Call Close before closing the
	// underlying file, or queued hops are lost.
	Writer *trace.Writer
}

// HopEvent is what a subscription delivers: a hop, plus whether this is a
// re-delivery of a Seq the subscriber may already hold. Updated is true
// exactly once per streaming hop — the finalization Update publishes when
// the stream closes — and consumers upsert by Seq rather than append.
type HopEvent struct {
	Hop     trace.Hop
	Updated bool
}

// Recorder is the single sink for captured hops: an in-memory replay ring,
// live fan-out to subscribers, and span-ownership tracking that lets the
// next hop name its caller.
type Recorder struct {
	mu        sync.Mutex
	opts      RecorderOpts
	ring      []trace.Hop
	ringBytes int64
	nextSeq   uint64
	subs      map[chan HopEvent]*subState
	owners    map[string]string // span id -> service name that opened it
	ownerQ    []string          // FIFO for owner eviction
	ownerCap  int
	// traceSes maps trace id -> session id, claimed at request START so
	// causal order holds even though hops are recorded at completion
	// (nested calls complete inner-first).
	traceSes map[string]string
	traceQ   []string
	traceCap int

	// Write-pipeline state. writeQ is nil when no Writer is configured;
	// closed flips once, in Close, after which nothing more is enqueued.
	// The counters answer "did anything fail to reach disk" — swallowing a
	// write error here used to be the recorder's one silent loss.
	writeQ        chan trace.Hop
	writerDone    chan struct{}
	closed        bool
	droppedWrites uint64
	writeErrors   uint64
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
	if opts.RingBytes <= 0 {
		opts.RingBytes = DefaultRingBytes
	}
	r := &Recorder{
		opts:     opts,
		subs:     map[chan HopEvent]*subState{},
		owners:   map[string]string{},
		ownerCap: 65536,
		traceSes: map[string]string{},
		traceCap: 65536,
	}
	if opts.Writer != nil {
		r.writeQ = make(chan trace.Hop, writeQueueCap)
		r.writerDone = make(chan struct{})
		go r.writeLoop()
	}
	return r
}

// writeLoop is the single writer goroutine: it drains the queue to disk in
// enqueue (= Seq) order and counts failures instead of discarding them.
// Exits when Close closes the queue, after draining what remains — that
// flush is what keeps a short-lived retrace capture from losing its tail.
func (r *Recorder) writeLoop() {
	defer close(r.writerDone)
	for h := range r.writeQ {
		if err := r.opts.Writer.Write(h); err != nil {
			r.mu.Lock()
			r.writeErrors++
			r.mu.Unlock()
		}
	}
}

// enqueueWriteLocked hands one hop to the writer goroutine without ever
// blocking — a full queue (stalled disk) drops the write and counts it.
// Caller holds r.mu, which is what keeps enqueue order equal to Seq order.
func (r *Recorder) enqueueWriteLocked(h trace.Hop) {
	if r.writeQ == nil || r.closed {
		return
	}
	select {
	case r.writeQ <- h:
	default:
		r.droppedWrites++
	}
}

// Close flushes the write queue and stops the writer goroutine. Call it
// before closing the Writer's underlying file — every shutdown path does
// (ensemble down, retrace capture close) — so nothing enqueued is lost.
// Recording after Close still works in memory; only persistence stops.
func (r *Recorder) Close() {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	q := r.writeQ
	r.mu.Unlock()
	if q != nil {
		close(q)
		<-r.writerDone
	}
}

// DroppedWrites reports how many hop persists were dropped because the
// write queue was full (a stalled or slow disk). Surfaced in
// GET /api/status — a non-zero value means hops.jsonl is incomplete.
func (r *Recorder) DroppedWrites() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.droppedWrites
}

// WriteErrors reports how many hop persists failed at the io.Writer (disk
// full, file gone). Surfaced in GET /api/status alongside DroppedWrites.
func (r *Recorder) WriteErrors() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.writeErrors
}

// payloadBytes and hopBytes estimate a hop's resident cost for the ring's
// byte budget. An estimate, not an accounting identity: bodies and headers
// dominate real hops by orders of magnitude, so the fixed struct overhead
// is a flat constant.
func payloadBytes(p trace.Payload) int64 {
	n := int64(len(p.Body) + len(p.BodyB64))
	for k, v := range p.Headers {
		n += int64(len(k) + len(v))
	}
	for _, c := range p.SetCookies {
		n += int64(len(c))
	}
	return n
}

func hopBytes(h trace.Hop) int64 {
	return 256 + int64(len(h.Path)+len(h.Err)) + payloadBytes(h.Req) + payloadBytes(h.Resp)
}

// evictLocked trims the ring's front until it is under both the count cap
// and the byte budget. A single over-budget hop still stays (the ring never
// evicts its only element to below one) — the capture cap bounds any one
// hop far under the budget anyway.
func (r *Recorder) evictLocked() {
	for len(r.ring) > 1 && (len(r.ring) > r.opts.Ring || r.ringBytes > r.opts.RingBytes) {
		r.ringBytes -= hopBytes(r.ring[0])
		r.ring = r.ring[1:]
	}
}

// Record assigns the next sequence number, scrubs, retains, and fans out.
// Schema is stamped here — the single place, so the ring, every subscriber
// (API/SSE), and the NDJSON file all agree; Writer.Write stamps it too but
// only as a no-op safety net for callers that bypass Record.
//
// A hop with Streaming set is retained and fanned out but NOT persisted
// yet: it is an open stream recorded at response-headers time, and the line
// on disk should be the finalized one Update writes when the stream closes
// — two NDJSON lines for one Seq would break every reader that treats the
// file as one record per hop.
func (r *Recorder) Record(h trace.Hop) trace.Hop {
	h.Schema = trace.SchemaVersion
	r.mu.Lock()
	redactor := r.opts.Redactor
	r.mu.Unlock()
	if redactor != nil {
		scrubbed, err := redactor.Hop(h)
		if err != nil {
			// Degrade, never drop: the redactor already destroyed whatever it
			// could not seal, so the hop is safe to keep — its bodies go, Err
			// names the failure, and the request is never killed over it.
			scrubbed = trace.DegradeHop(scrubbed, err)
		}
		h = scrubbed
	}
	r.mu.Lock()
	r.nextSeq++
	h.Seq = r.nextSeq
	r.ring = append(r.ring, h)
	r.ringBytes += hopBytes(h)
	r.evictLocked()
	if !h.Streaming {
		r.enqueueWriteLocked(h)
	}
	r.fanOutLocked(HopEvent{Hop: h})
	r.mu.Unlock()
	return h
}

// Update finalizes a hop in place: same Seq, new content — how a streaming
// hop recorded at response-headers time gets its duration, final body and
// truncation when the stream closes. The ring slot is replaced (so
// Snapshot and late subscribers see the finalized hop), the finalized line
// is persisted, and every subscriber receives an Updated event to upsert
// by Seq. h.Seq must be the value Record assigned; a Seq the ring already
// evicted still persists and fans out — the update is real even if the
// in-memory slot is gone.
func (r *Recorder) Update(h trace.Hop) trace.Hop {
	h.Schema = trace.SchemaVersion
	r.mu.Lock()
	redactor := r.opts.Redactor
	r.mu.Unlock()
	if redactor != nil {
		scrubbed, err := redactor.Hop(h)
		if err != nil {
			scrubbed = trace.DegradeHop(scrubbed, err)
		}
		h = scrubbed
	}
	r.mu.Lock()
	// Seqs are strictly increasing in the ring, so the slot is a binary
	// search away.
	i := sort.Search(len(r.ring), func(i int) bool { return r.ring[i].Seq >= h.Seq })
	if i < len(r.ring) && r.ring[i].Seq == h.Seq {
		r.ringBytes += hopBytes(h) - hopBytes(r.ring[i])
		r.ring[i] = h
		r.evictLocked()
	}
	r.enqueueWriteLocked(h)
	r.fanOutLocked(HopEvent{Hop: h, Updated: true})
	r.mu.Unlock()
	return h
}

// fanOutLocked delivers one event to every subscriber, non-blocking.
// A fresh hop already delivered (Seq <= cursor) is skipped; an Updated
// event is never skipped by cursor — it re-delivers a Seq on purpose, and
// consumers upsert rather than append.
func (r *Recorder) fanOutLocked(ev HopEvent) {
	for ch, st := range r.subs {
		if !ev.Updated {
			if ev.Hop.Seq <= st.delivered {
				continue
			}
		}
		select {
		case ch <- ev:
			if ev.Hop.Seq > st.delivered {
				st.delivered = ev.Hop.Seq
			}
		default:
			// Slow subscriber: drop rather than block the capture path.
			// Counted so a subscriber (e.g. SessionManager) can detect and
			// surface the loss instead of silently under-reporting.
			st.dropped++
		}
	}
}

// SetRedactor swaps the redaction rules applied to every hop from this call
// onward — e.g. after `ensemble up` reconciles a config whose redact list
// changed. Already-recorded hops are not retroactively scrubbed.
func (r *Recorder) SetRedactor(redactor HopRedactor) {
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
// The returned dropped func reports how many events Record/Update have had
// to drop for this subscription because its buffer was full (a slow
// consumer) — callers that need to know their capture may be incomplete
// (e.g. SessionManager, for verdict purposes) should poll it. The returned
// cancel must be called to release the subscription.
func (r *Recorder) Subscribe(cursor uint64) (ch <-chan HopEvent, dropped func() uint64, cancel func()) {
	r.mu.Lock()
	// Buffer must hold the full replay: it is filled under the lock, so a
	// reader cannot drain it concurrently and a tight buffer would deadlock.
	c := make(chan HopEvent, len(r.ring)+256)
	st := &subState{delivered: cursor}
	for _, h := range r.ring {
		if h.Seq > cursor {
			c <- HopEvent{Hop: h}
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
