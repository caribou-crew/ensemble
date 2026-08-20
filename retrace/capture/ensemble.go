package capture

import (
	"context"
	"fmt"
	"os"
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
	StartSession(ctx context.Context, id, entry string) (edgeAddr string, err error)
	SessionHops(ctx context.Context, id string) ([]trace.Hop, error)
	EndSession(ctx context.Context, id string) (EndReport, error)
}

// EndReport is DECODED from ensemble's DELETE /api/sessions/{id} response.
type EndReport struct {
	Hops    int           `json:"hops"`
	Verdict trace.Verdict `json:"verdict"`
	Reasons []string      `json:"reasons"`
}

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
	edge, err := c.StartSession(context.Background(), runID, entry)
	if err != nil {
		_ = os.RemoveAll(p.RunDir)
		return nil, fmt.Errorf("register session with ensemble: %w", err)
	}
	s := &Session{
		Paths: p, RunID: runID, Mode: runs.ModeEnsemble, StartedAt: now(),
		ProxyURL: "http://" + edge, ens: c,
		redact: o.Redact, maxBody: bodyLimit(o.MaxBody),
	}
	if err := s.startMarkerDoor(now); err != nil {
		_, _ = c.EndSession(context.Background(), runID)
		_ = os.RemoveAll(p.RunDir)
		return nil, err
	}
	return s, nil
}

// drainPoll/drainWindow bound the wait for hops that complete after the
// test command exits. Nested calls are RECORDED inner-first but they all
// have to finish before the outermost one does, so "the count stopped
// changing" is a sound stop condition — and the cap keeps a wedged upstream
// from hanging CI.
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
func (s *Session) Drain(ctx context.Context) error {
	if s.ens == nil {
		return nil
	}
	deadline := time.Now().Add(drainWindow)
	last := -1
	for {
		hops, err := s.ens.SessionHops(ctx, s.RunID)
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

	red := trace.NewRedactor(s.redact, s.maxBody)
	written := 0
	if err := writeHops(s.Paths.HopsPath, s.hops, red, func(trace.Hop) bool { return true }, &written); err != nil {
		return err
	}
	wire := 0
	if err := writeHops(s.Paths.WirePath, s.hops, red, isClientEdge, &wire); err != nil {
		return err
	}

	// EndSession runs only now, with the recording already on disk.
	rep, err := s.ens.EndSession(context.Background(), s.RunID)
	if err != nil {
		s.trustNotes = append(s.trustNotes, "ending the ensemble session failed: "+err.Error())
		return nil // the recording is already on disk; never lose it over a teardown error
	}
	s.endReport, s.ended = rep, true
	if rep.Hops > written {
		s.trustNotes = append(s.trustNotes,
			fmt.Sprintf("%d hop(s) arrived after the drain window and are missing from this recording", rep.Hops-written))
		// A recording known to be short is not "ok" whatever ensemble says
		// about its own session — the verdict on disk describes THIS file.
		s.endReport.Verdict = s.endReport.Verdict.Worse(trace.VerdictSuspect)
	}
	return nil
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
