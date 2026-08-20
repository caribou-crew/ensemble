package proxy

import (
	"fmt"
	"sync"

	"github.com/caribou-crew/ensemble/core/trace"
)

// Session is one recording run: an ephemeral client-edge listener that
// stamps every entering request with encore-run baggage, plus the hops
// partitioned to it and a capture-trust verdict.
type Session struct {
	ID       string
	EdgeAddr string

	mu      sync.Mutex
	hops    []trace.Hop
	verdict trace.Verdict
	reasons []string

	stopEdge func()
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

// SessionManager partitions the recorder's hop stream into sessions and
// detects propagation gaps. One subscription goroutine sees every hop in
// sequence order:
//
//   - a hop carrying encore-run baggage goes to its session (and the
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
	entries  map[string]bool // target names clients legitimately call context-less

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

func (m *SessionManager) loop(ch <-chan trace.Hop) {
	defer close(m.done)
	var lastDropped uint64
	for h := range ch {
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
		m.route(h)
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

func (m *SessionManager) route(h trace.Hop) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if h.Session != "" {
		ses := m.sessions[h.Session]
		if ses == nil {
			return // session already ended; late hop is dropped
		}
		ses.mu.Lock()
		ses.hops = append(ses.hops, h)
		ses.mu.Unlock()
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
				ses.mu.Lock()
				ses.hops = append(ses.hops, h)
				ses.mu.Unlock()
			}
			return
		}
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
// intercept port fronting entryUpstream that stamps encore-run baggage on
// everything entering it.
func (m *SessionManager) Start(id, entryName, entryUpstream string) (*Session, error) {
	ses := &Session{
		ID:      id,
		verdict: trace.VerdictOK,
	}
	addr, stop, err := m.proxy.ServeStoppable(Target{
		Name:          entryName,
		Listen:        "127.0.0.1:0",
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
		return nil, fmt.Errorf("session %q already active", id)
	}
	m.sessions[id] = ses
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
// Returns the finalized session, or nil if unknown.
func (m *SessionManager) End(id string) *Session {
	m.mu.Lock()
	ses := m.sessions[id]
	delete(m.sessions, id)
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
