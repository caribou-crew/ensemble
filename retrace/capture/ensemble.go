package capture

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/caribou-crew/ensemble/core/proxy"
	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

// EnsembleClient is the slice of ensemble's control plane retrace attaches
// to. It is an interface, not a concrete client, so package capture never
// imports package ensemble.
type EnsembleClient interface {
	Health(ctx context.Context) error
	StartSession(ctx context.Context, req SessionRequest) (edgeAddr string, err error)
	SessionHops(ctx context.Context, id string) ([]trace.Hop, error)
	EndSession(ctx context.Context, id string) (EndReport, error)
	// Stack fingerprints what the backend currently is: a value per service
	// and the last applied seed. A nil return is "this control plane told us
	// nothing", which the manifest records as absence — never as an empty
	// stack, which would compare equal to every other run that recorded
	// nothing.
	Stack(ctx context.Context) (*runs.Stack, error)
}

// SessionRequest is what StartSession registers with ensemble's control
// plane (POST /api/sessions). A struct rather than positional parameters so
// the next field (this is already the second addition) doesn't require a
// signature sweep across every implementation and call site again. Host is
// optional — empty keeps ensemble's own default (127.0.0.1); see
// design.md §6.1.2's proxy.host addendum.
type SessionRequest struct {
	ID    string
	Entry string
	Host  string
	// Port is the fixed TCP port ensemble's session should bind its
	// client-edge listener on. Zero keeps ensemble's own default (an
	// ephemeral port); see design.md §6.1.2's proxy.port addendum.
	Port int
}

// EndReport is DECODED from ensemble's DELETE /api/sessions/{id} response.
type EndReport struct {
	Hops    int           `json:"hops"`
	Verdict trace.Verdict `json:"verdict"`
	Reasons []string      `json:"reasons"`
}

// controlTimeout bounds a single call to ensemble's control plane outside
// the drain loop (StartSession, EndSession). The underlying http.Client
// carries its own Timeout as a last-resort backstop (see cmd/retrace's
// client.go), but a call-scoped context here means a wedged POST/DELETE
// fails within a predictable bound instead of only "eventually, whenever
// the client-level backstop fires" — see Major 1 of the Task 5 fix-round-1
// review.
const controlTimeout = 5 * time.Second

func StartAttached(o Options, c EnsembleClient, entry string) (*Session, error) {
	now := o.Now
	if now == nil {
		now = time.Now
	}
	runID := runs.NewRunID(now(), gitSHA(o.Cwd))
	p, err := runs.Create(runs.RunsRoot(o.Cwd), o.App, o.Flow, runID)
	if err != nil {
		return nil, err
	}
	startCtx, cancel := context.WithTimeout(context.Background(), controlTimeout)
	edge, err := c.StartSession(startCtx, SessionRequest{ID: runID, Entry: entry, Host: o.Host, Port: o.Port})
	cancel()
	if err != nil {
		_ = os.RemoveAll(p.RunDir)
		return nil, fmt.Errorf("register session with ensemble: %w", err)
	}
	// Read at the START of the run: this is the stack the test is about to
	// exercise. Reading it at the end would report whatever the stack became,
	// which after a mid-run redeploy is not what produced the recording.
	//
	// A failure here is not fatal. A control plane that cannot answer leaves
	// the run with no stack record, and a diff with no record on one side
	// reports no stack change — the same absence-is-not-evidence rule the
	// screen-geometry guard follows. Refusing to record a run over a
	// diagnostic would be the worse trade.
	stackCtx, stackCancel := context.WithTimeout(context.Background(), controlTimeout)
	stack, stackErr := c.Stack(stackCtx)
	stackCancel()

	s := &Session{
		Paths: p, RunID: runID, App: o.App, Flow: o.Flow, Mode: runs.ModeEnsemble, StartedAt: now(),
		stack: stack, stackErr: stackErr,
		ProxyURL:    "http://" + edge,
		UpstreamURL: strings.TrimRight(o.Upstream, "/"),
		ens:         c,
		redact:      o.Redact, maxBody: bodyLimit(o.MaxBody),
	}
	if err := s.startMarkerDoor(now); err != nil {
		endCtx, endCancel := context.WithTimeout(context.Background(), controlTimeout)
		_, _ = c.EndSession(endCtx, runID)
		endCancel()
		_ = os.RemoveAll(p.RunDir)
		return nil, err
	}
	return s, nil
}

// drainPoll/drainWindow bound the wait for hops that complete after the
// test command exits. Nested calls are RECORDED inner-first but they all
// have to finish before the outermost one does, so "the count stopped
// changing" is a sound stop condition — and the cap keeps a wedged upstream
// from hanging CI: drainWindow bounds the time BETWEEN polls (the stability
// check below) AND, separately, each individual poll gets its own
// drainWindow-length context deadline, so a single call that never answers
// cannot hang past the window either. This is a fresh timeout per call, not
// a shrinking deadline tied to Drain's overall elapsed time — a
// slow-but-answering poll near the end of the window still gets to complete
// and can be caught by the between-polls check below, rather than being
// force-canceled mid-flight and reported as an error.
const (
	drainPoll   = 100 * time.Millisecond
	drainWindow = 2 * time.Second
)

// Drain polls until the hop count is stable across two consecutive polls or
// the window expires, then snapshots. It must run BEFORE EndSession:
// ensemble's SessionManager drops hops for a session it no longer knows
// ("session already ended; late hop is dropped"), so ending first silently
// loses every downstream call still in flight when the command exited.
// No-op in standalone mode — the local Recorder already has everything.
//
// Draining first NARROWS the loss window; it does not CLOSE it. A hop that
// ensemble routes after EndSession's map-delete (not merely after the last
// poll here) is still dropped, silently and with no counter anywhere in
// ensemble today — that residual window is real and cannot be closed from
// inside retrace. See Close's reconciliation, which can only catch the
// narrower gap between the last poll and EndSession, not the gap after it.
func (s *Session) Drain(ctx context.Context) error {
	if s.ens == nil {
		return nil
	}
	deadline := time.Now().Add(drainWindow)
	last := -1
	for {
		pollCtx, cancel := context.WithTimeout(ctx, drainWindow)
		hops, err := s.ens.SessionHops(pollCtx, s.RunID)
		cancel()
		if err != nil {
			return err
		}
		s.hops = hops
		if len(hops) == last {
			return nil
		}
		last = len(hops)
		if time.Now().After(deadline) {
			s.trustNotes = append(s.trustNotes,
				fmt.Sprintf("hops were still arriving when the %s drain window expired — the recording may be truncated", drainWindow))
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(drainPoll):
		}
	}
}

// NoteDrainFailure records that Drain returned an error, so the failure
// survives in the durable trust record (TrustNotes, which Task 6 reads) and
// not only in the caller's own stderr line — stderr is not a durable
// artifact.
func (s *Session) NoteDrainFailure(err error) {
	s.trustNotes = append(s.trustNotes, "draining ensemble hops failed: "+err.Error())
}

// Close writes hops.jsonl and wire.jsonl, then ends the session and
// reconciles the counts. Everything on disk goes through a Redactor first:
// ensemble redacted on capture, but a recording is committed and shared, so
// retrace re-applies its OWN configured key list rather than trusting the
// producer's.
func (s *Session) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	if s.markerSrv != nil {
		s.markerSrv.Close()
	}
	if s.ens == nil {
		// Standalone: retrace owns the listener and the wire file.
		if s.stopProxy != nil {
			s.stopProxy()
		}
		if s.wireFile == nil {
			return nil
		}
		return s.wireFile.Close()
	}

	// writeErr is captured, not returned immediately: persisting hops and
	// releasing the remote session are independent obligations. A write
	// failure must not skip EndSession — ensemble's SessionManager.Start has
	// already opened a client-edge listener and inserted this session into
	// m.sessions, and only End releases them. Skipping End here leaks that
	// listener and keeps `route`'s heuristics firing against a session that
	// will never end for the rest of `ensemble up`'s lifetime (Major 2).
	red := trace.NewRedactor(s.redact, s.maxBody)
	written := 0
	writeErr := writeHops(s.Paths.HopsPath, s.hops, red, func(trace.Hop) bool { return true }, &written)
	wire := 0
	if writeErr == nil {
		writeErr = writeHops(s.Paths.WirePath, s.hops, red, isClientEdge, &wire)
	}

	// EndSession runs even when the writes above failed — see above. Both
	// errors matter: the write failure is surfaced to the caller below, and
	// a teardown failure is noted but never allowed to discard a recording
	// that already reached disk.
	endCtx, cancel := context.WithTimeout(context.Background(), controlTimeout)
	rep, err := s.ens.EndSession(endCtx, s.RunID)
	cancel()
	if err != nil {
		s.trustNotes = append(s.trustNotes, "ending the ensemble session failed: "+err.Error())
		return writeErr // the recording (whatever wrote) is already on disk; never lose it over a teardown error
	}
	s.endReport, s.ended = rep, true
	if rep.Hops > written {
		s.trustNotes = append(s.trustNotes,
			fmt.Sprintf("%d hop(s) arrived after the drain window and are missing from this recording", rep.Hops-written))
		// A recording known to be short is not "ok" whatever ensemble says
		// about its own session — the verdict on disk describes THIS file.
		s.endReport.Verdict = s.endReport.Verdict.Worse(trace.VerdictSuspect)
	}
	return writeErr
}

// isClientEdge selects the hops wire.jsonl holds: those whose caller is not
// a service ensemble proxies. core/proxy fills Hop.From from the recorder's
// span-owner map, so From == "" means "a client, or an unproxied caller" —
// exactly the client edge.
func isClientEdge(h trace.Hop) bool { return h.From == "" }

func writeHops(path string, hops []trace.Hop, red *trace.Redactor, keep func(trace.Hop) bool, n *int) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := trace.NewWriter(f)
	for _, h := range hops {
		if !keep(h) {
			continue
		}
		if err := w.Write(red.Hop(h)); err != nil {
			return err
		}
		*n++
	}
	return nil
}

// bodyLimit normalizes Options.MaxBody. Zero means "the caller did not
// choose", which must resolve to the proxy's capture cap, not to
// trace.Redactor's "no cap at all" reading of a non-positive value.
func bodyLimit(n int) int {
	if n <= 0 {
		return proxy.CaptureLimit
	}
	return n
}

// EndVerdict is ensemble's own capture-trust verdict for this session.
func (s *Session) EndVerdict() trace.Verdict {
	if s.ens == nil {
		return ""
	}
	if !s.ended || s.endReport.Verdict == "" {
		return trace.VerdictSuspect
	}
	return s.endReport.Verdict
}

func (s *Session) EndReasons() []string {
	return append([]string(nil), s.endReport.Reasons...)
}

func (s *Session) TrustNotes() []string {
	return append([]string(nil), s.trustNotes...)
}
