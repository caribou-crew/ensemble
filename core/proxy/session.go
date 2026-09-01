package proxy

import (
	"errors"
	"fmt"
	"strconv"
	"sync"

	"github.com/caribou-crew/ensemble/core/trace"
)

// ErrSessionActive marks an id that is already registered. Wrapped, not
// returned bare, so a caller identifies it with errors.Is rather than
// string-matching — ensemble/server's handleSessionStart maps this
// specifically to 409 Conflict, and every OTHER Start failure (a bad
// proxy.host, a bind failure) is a different kind of caller mistake and
// maps to 400 instead.
var ErrSessionActive = errors.New("session already active")

// Session is one recording run: an ephemeral client-edge listener that
// stamps every entering request with retrace-run baggage, plus the hops
// partitioned to it and a capture-trust verdict.
type Session struct {
	ID       string
	EdgeAddr string

	mu      sync.Mutex
	hops    []trace.Hop
	verdict trace.Verdict
	reasons []string
	// droppedHops counts hops that provably belonged to this session but
	// were not kept — today that means hops routed after End. Zero must be
	// provable: any hop discarded on the session's account increments this
	// instead of vanishing, so "no hops lost" and "hops lost silently" never
	// serialize identically (roadmap F.3).
	droppedHops uint64

	stopEdge func()
}

// DroppedHops returns how many of this session's hops were discarded
// rather than kept. See the field comment for what counts.
func (s *Session) DroppedHops() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.droppedHops
}

func (s *Session) noteDropped() {
	s.mu.Lock()
	s.droppedHops++
	s.mu.Unlock()
}

// noteHop stores one hop attributed to this session. updated means the hop
// is a finalization of a Seq already delivered (a streaming hop closing) —
// upsert in place so the session's record carries the final body and
// duration, not the headers-time snapshot; if the original was never seen
// (delivered before this session subscribed its slice — or dropped) the
// finalized hop is simply appended, which is the complete version anyway.
// A hop the proxy refused as an unsupported protocol degrades the verdict:
// the traffic provably reached a captured port and was NOT captured, and a
// recording missing it must say so (protocol-guardrails spec).
func (s *Session) noteHop(h trace.Hop, updated bool) {
	if h.Unsupported != "" {
		s.degrade(trace.VerdictDegraded,
			fmt.Sprintf("unsupported protocol: a %s request to %s was refused with 501 and is not captured", h.Unsupported, h.To))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if updated {
		for i := len(s.hops) - 1; i >= 0; i-- {
			if s.hops[i].Seq == h.Seq {
				s.hops[i] = h
				return
			}
		}
	}
	s.hops = append(s.hops, h)
}

// Hops returns the hops attributed to this session so far, oldest first.
func (s *Session) Hops() []trace.Hop {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]trace.Hop, len(s.hops))
	copy(out, s.hops)
	return out
}

// Verdict returns the session's capture-trust rating and its reasons.
func (s *Session) Verdict() (trace.Verdict, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	reasons := make([]string, len(s.reasons))
	copy(reasons, s.reasons)
	return s.verdict, reasons
}

func (s *Session) degrade(v trace.Verdict, reason string) {
	s.mu.Lock()
	s.verdict = s.verdict.Worse(v)
	for _, r := range s.reasons {
		if r == reason {
			s.mu.Unlock()
			return
		}
	}
	s.reasons = append(s.reasons, reason)
	s.mu.Unlock()
}

// endedCap bounds how many ended sessions the manager retains for
// late-hop accounting (see SessionManager.ended). 128 comfortably covers
// every session a single `ensemble up` realistically ends; beyond it the
// oldest ended session stops counting its stragglers — bounded memory wins
// over a perfect count for sessions nobody has asked about in ages.
const endedCap = 128

// SessionManager partitions the recorder's hop stream into sessions and
// detects propagation gaps. One subscription goroutine sees every hop in
// sequence order:
//
//   - a hop carrying retrace-run baggage goes to its session (and the
//     session learns the hop's trace id);
//   - a hop WITHOUT session baggage whose trace id belongs to a session is
//     a proven gap — some service forwarded traceparent but dropped
//     baggage — and degrades that session, naming the offender;
//   - a hop with no inherited context arriving at a non-entry target while
//     sessions are active is unattributable under parallelism and marks
//     every active session suspect (it may equally be ambient traffic —
//     which is why this is "suspect", not "degraded").
type SessionManager struct {
	proxy *Proxy
	rec   *Recorder

	mu       sync.Mutex
	sessions map[string]*Session
	// ended retains sessions after End, bounded by endedCap (endedQ is the
	// eviction FIFO), so a hop routed to a session that already ended still
	// lands on THAT session's droppedHops counter instead of vanishing.
	// End deletes from the live map — routing must stop — but the counter
	// has to stay reachable or "routed after End" would be exactly the
	// silent loss droppedHops exists to make impossible (roadmap F.3).
	ended   map[string]*Session
	endedQ  []string
	entries map[string]bool // target names clients legitimately call context-less

	dropped   func() uint64 // cumulative hops the Recorder has dropped for our subscription
	cancelSub func()
	done      chan struct{}
}

// NewSessionManager starts the partitioning loop. entryTargets names the
// services clients call directly (context-less arrivals there are normal).
func NewSessionManager(p *Proxy, rec *Recorder, entryTargets []string) *SessionManager {
	m := &SessionManager{
		proxy:    p,
		rec:      rec,
		sessions: map[string]*Session{},
		ended:    map[string]*Session{},
		entries:  map[string]bool{},
		done:     make(chan struct{}),
	}
	for _, e := range entryTargets {
		m.entries[e] = true
	}
	ch, dropped, cancel := rec.Subscribe(0)
	m.dropped = dropped
	m.cancelSub = cancel
	go m.loop(ch)
	return m
}

// dropReason is the fixed (dedup-friendly, per Session.degrade) reason
// string used whenever the Recorder reports it dropped hops for our
// subscription — see loop's drop check.
const dropReason = "recorder dropped hops for this subscriber (buffer full, slow consumer) — capture is incomplete"

func (m *SessionManager) loop(ch <-chan HopEvent) {
	defer close(m.done)
	var lastDropped uint64
	for ev := range ch {
		// The Recorder's fan-out to our subscription channel is
		// non-blocking (Record must never stall on a slow subscriber), so a
		// full buffer silently drops hops rather than notifying us
		// directly. We can't know a drop happened the instant it does, but
		// checking the cumulative counter on every hop we DO receive
		// catches it promptly — SessionManager's job is exactly to notice
		// capture loss, and reporting verdict "ok" on an incomplete capture
		// is worse than no verdict.
		if d := m.dropped(); d != lastDropped {
			lastDropped = d
			m.degradeActiveSessions(dropReason)
		}
		m.route(ev)
	}
}

// degradeActiveSessions marks every currently active session degraded with
// reason — used when the Recorder reports it dropped hops for our
// subscription: we can't know which session(s) the dropped hop(s) belonged
// to, so every session active right now must be treated as possibly
// incomplete.
func (m *SessionManager) degradeActiveSessions(reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, ses := range m.sessions {
		ses.degrade(trace.VerdictDegraded, reason)
	}
}

func (m *SessionManager) route(ev HopEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()
	h := ev.Hop

	if h.Session != "" {
		ses := m.sessions[h.Session]
		if ses == nil {
			// Session already ended. The finalization of a hop the session
			// kept while live is not a lost hop — only a fresh one counts.
			if !ev.Updated {
				if ended := m.ended[h.Session]; ended != nil {
					ended.noteDropped()
				}
			}
			return
		}
		ses.noteHop(h, ev.Updated)
		return
	}

	// A hop with no trace id never passed through the proxy's request
	// handling — every real proxied request gets one, minted or inherited
	// (see proxy.go's handler: trace.ParseCtx always yields a non-empty
	// TraceID). This is how a control-plane annotation looks (recorded
	// straight into the Recorder for an API mutation, not a proxied call —
	// see ensemble/server's withAnnotation). Without a provable trace it
	// can't be shown to belong to, or be a gap in, any traced chain, so it
	// must never influence a session's capture-trust verdict — skip it
	// before either heuristic below gets a chance to misfire on it.
	if h.TraceID == "" {
		return
	}

	if len(m.sessions) == 0 {
		return // pure ambient, nobody recording
	}

	// Proven gap: the trace was claimed by a session at request start, but
	// this hop arrived without the session baggage.
	if h.TraceID != "" {
		if owner := m.rec.TraceSession(h.TraceID); owner != "" {
			if ses := m.sessions[owner]; ses != nil {
				at := h.From
				if at == "" {
					at = "the caller of " + h.To
				}
				ses.degrade(trace.VerdictDegraded,
					fmt.Sprintf("propagation gap at %s: traceparent forwarded but baggage dropped before %s", at, h.To))
				// The hop provably belongs to this session — keep it.
				ses.noteHop(h, ev.Updated)
			}
			return
		}
	}

	// A finalization is not a new arrival: the heuristic below already had
	// its chance at the original event for this Seq.
	if ev.Updated {
		return
	}

	// Heuristic: context-less arrival mid-chain during active sessions.
	if h.From == "" && !m.entries[h.To] {
		for _, ses := range m.sessions {
			ses.degrade(trace.VerdictSuspect,
				fmt.Sprintf("unattributed traffic at %s while session active — a service may be dropping trace headers, or ambient traffic hit a mid-chain port", h.To))
		}
	}
}

// Start registers a session and opens its client-edge listener: an extra
// intercept port fronting entryUpstream that stamps retrace-run baggage on
// everything entering it. host is the hostname the listener binds on AND
// is advertised as; empty means the default, "127.0.0.1" — unchanged
// behavior. A non-default host must resolve loopback-only, enforced by
// ServeStoppable (see design.md §6.1.2's proxy.host addendum), not
// re-validated here. port is the fixed TCP port to bind; zero means the
// default, an OS-chosen ephemeral port — unchanged behavior. A non-zero
// port already in use surfaces ServeStoppable's own bind error unchanged;
// this never silently falls back to a different port (design.md §6.1.2's
// proxy.port addendum).
//
// The duplicate-id check runs BEFORE the edge listener binds. Binding
// first meant a colliding Start briefly held a fixed proxy.port the live
// session was about to need back, and with an ephemeral port it opened a
// listener whose only future was being torn down — a caller retrying a
// 409 in a loop could starve the port it was colliding over. The check is
// repeated under the lock after the bind: two concurrent Starts with the
// same never-before-seen id both pass the pre-check, and the second one
// must still lose.
func (m *SessionManager) Start(id, entryName, entryUpstream, host string, port int) (*Session, error) {
	m.mu.Lock()
	if _, exists := m.sessions[id]; exists {
		m.mu.Unlock()
		return nil, fmt.Errorf("session %q already active: %w", id, ErrSessionActive)
	}
	m.mu.Unlock()

	if host == "" {
		host = "127.0.0.1"
	}
	portStr := "0"
	if port != 0 {
		portStr = strconv.Itoa(port)
	}
	ses := &Session{
		ID:      id,
		verdict: trace.VerdictOK,
	}
	addr, stop, err := m.proxy.ServeStoppable(Target{
		Name:          entryName,
		Listen:        host + ":" + portStr,
		Upstream:      entryUpstream,
		InjectBaggage: map[string]string{trace.BaggageSession: id},
	})
	if err != nil {
		return nil, err
	}
	ses.EdgeAddr = addr
	ses.stopEdge = stop

	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.sessions[id]; exists {
		stop()
		return nil, fmt.Errorf("session %q already active: %w", id, ErrSessionActive)
	}
	m.sessions[id] = ses
	// A restarted id sheds its ended predecessor: routing must find the
	// LIVE session, and the old counters described a different run.
	delete(m.ended, id)
	return ses, nil
}

// Get returns id's active session, or nil if no session is active under
// that id (never started, or already ended). Used by read-only consumers
// (e.g. the control API's hop-tail endpoint) that must not end the session
// as a side effect of looking it up.
func (m *SessionManager) Get(id string) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[id]
}

// End closes the session's edge listener and detaches it from routing.
// Returns the finalized session, or nil if unknown. The session is
// retained in the bounded ended map so hops still in flight when the map
// entry vanished land on droppedHops instead of nowhere — see the ended
// field's comment.
func (m *SessionManager) End(id string) *Session {
	m.mu.Lock()
	ses := m.sessions[id]
	delete(m.sessions, id)
	if ses != nil {
		if _, already := m.ended[id]; !already {
			m.endedQ = append(m.endedQ, id)
			if len(m.endedQ) > endedCap {
				evict := m.endedQ[0]
				m.endedQ = m.endedQ[1:]
				delete(m.ended, evict)
			}
		}
		m.ended[id] = ses
	}
	m.mu.Unlock()
	if ses == nil {
		return nil
	}
	ses.stopEdge()
	return ses
}

// Close ends every session and stops the partitioning loop.
func (m *SessionManager) Close() {
	m.mu.Lock()
	for id, ses := range m.sessions {
		ses.stopEdge()
		delete(m.sessions, id)
	}
	m.mu.Unlock()
	m.cancelSub()
	<-m.done
}
