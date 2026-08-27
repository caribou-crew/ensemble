package capture

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caribou-crew/ensemble/core/proxy"
	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

// hop builds a minimal recorded hop. `to` is the callee; `from` defaults to
// "" (the client edge) unless a test sets it — see isClientEdge.
func hop(seq uint64, to string) trace.Hop {
	return trace.Hop{
		Schema: trace.SchemaVersion, Seq: seq, TraceID: "t1",
		To: to, Method: "GET", Path: "/" + to, Status: 200,
		T: trace.Timings{Start: time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)},
	}
}

// attachedSessionFor starts an ensemble-attached session against c, rooted
// in a per-test temp directory so nothing leaks between tests.
func attachedSessionFor(t *testing.T, c EnsembleClient) *Session {
	t.Helper()
	s, err := StartAttached(Options{
		Cwd: t.TempDir(), App: "web", Flow: "checkout",
		Now: func() time.Time { return time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC) },
	}, c, "bff")
	if err != nil {
		t.Fatalf("StartAttached: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// fakeEnsemble stands in for ensemble's control plane. It reproduces the
// ordering this task exists to defend against — a hop that lands after the
// test command exits — deterministically, by appending `late` during the
// first SessionHops call rather than after a wall-clock delay.
type fakeEnsemble struct {
	mu        sync.Mutex
	hops      []trace.Hop
	late      *trace.Hop // injected on the first poll, then cleared
	polls     int
	endCalled bool
	hopsAtEnd int // len(hops) when EndSession was called — the ordering assertion
	// overreport, when > 0, is what EndSession claims it counted regardless
	// of how many hops were ever served — the shape of a hop that landed
	// after the drain window closed. A value, not a sleep.
	overreport int
	// ended models the one ensemble behaviour this task exists to defend
	// against (core/proxy/session.go's SessionManager.End): once set, the
	// session is gone from ensemble's point of view. SessionHops and
	// EndSession both refuse afterwards (mirroring the real 404 "session
	// %q not found" — see routes.go's handleSessionHops/handleSessionEnd),
	// and push becomes a no-op — a fake that keeps serving hops for an
	// ended session cannot fail when a caller ends the session too early.
	ended bool
	// stack/stackErr are what Stack returns; stackCalls counts the asks, so a
	// test can pin that the fingerprint is read once at session start and not
	// polled.
	stack      *runs.Stack
	stackErr   error
	stackCalls int
}

func (f *fakeEnsemble) push(h trace.Hop) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ended {
		return
	}
	f.hops = append(f.hops, h)
}

func (f *fakeEnsemble) Health(context.Context) error { return nil }

// Stack returns whatever the test loaded into stack/stackErr — nil/nil by
// default, which models the honest common case: a control plane with nothing
// to report, not one that fails.
func (f *fakeEnsemble) Stack(context.Context) (*runs.Stack, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stackCalls++
	return f.stack, f.stackErr
}

func (f *fakeEnsemble) StartSession(_ context.Context, req SessionRequest) (string, error) {
	return "127.0.0.1:0", nil
}

// SessionHops takes the id the interface declares, and returns a COPY: the
// caller keeps the slice past the lock, so handing out the backing array
// would be a data race the -race gate catches on a good day and misses on
// a bad one.
func (f *fakeEnsemble) SessionHops(_ context.Context, id string) ([]trace.Hop, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ended {
		return nil, errors.New(`404: session "x" not found`)
	}
	f.polls++
	if f.polls == 1 && f.late != nil {
		f.hops = append(f.hops, *f.late)
		f.late = nil
	}
	return append([]trace.Hop(nil), f.hops...), nil
}

// EndSession records how many hops existed at teardown. Everything it
// touches is under f.mu — writing f.endCalled lock-free while a goroutine
// appends to f.hops under the lock is a data race in the one task gated on
// -race.
func (f *fakeEnsemble) EndSession(_ context.Context, id string) (EndReport, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ended {
		return EndReport{}, errors.New(`409: session "x" already ended`)
	}
	f.endCalled, f.hopsAtEnd = true, len(f.hops)
	n := len(f.hops)
	if f.overreport > 0 {
		n = f.overreport
	}
	f.ended = true
	return EndReport{Hops: n, Verdict: trace.VerdictOK}, nil
}

func TestDrainWaitsForLateHopsBeforeEndingTheSession(t *testing.T) {
	late := hop(2, "catalog")
	f := &fakeEnsemble{late: &late}
	f.push(hop(1, "edge"))

	s := attachedSessionFor(t, f)
	if err := s.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	hops, _, _ := runs.ReadHops(s.Paths.HopsPath)
	if len(hops) != 2 {
		t.Fatalf("Drain must not end the session before late hops land: got %d, want 2", len(hops))
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	// The ordering, asserted directly rather than inferred from the count:
	// ensemble's SessionManager drops hops for a session it has already
	// ended, so EndSession must observe the fully drained state.
	if !f.endCalled || f.hopsAtEnd != 2 {
		t.Fatalf("EndSession saw %d hop(s) (called=%v); it must run AFTER the drain", f.hopsAtEnd, f.endCalled)
	}
	// Stability needs two agreeing polls; one poll would mean the loop
	// stopped at the first answer it got.
	if f.polls < 2 {
		t.Fatalf("polls = %d; the drain must confirm stability across two polls", f.polls)
	}
}

// A fake whose EndReport.Hops is 3 while only 2 were ever served → the
// shortfall is a value the fake returns, so there is no sleep and no
// timing dependency anywhere in this test.
func TestHopsArrivingAfterTheDrainWindowDegradeTheVerdict(t *testing.T) {
	f := &fakeEnsemble{overreport: 3}
	f.push(hop(1, "edge"))
	f.push(hop(2, "catalog"))

	s := attachedSessionFor(t, f)
	if err := s.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	notes := strings.Join(s.TrustNotes(), "\n")
	if !strings.Contains(notes, "1 hop(s) arrived after the drain window") {
		t.Fatalf("TrustNotes = %q, want it to name the 1-hop shortfall", notes)
	}
	// "At least suspect": ensemble said ok, but retrace knows the recording
	// is short, and the recording's own verdict is what Task 6 gates on.
	if got := s.EndVerdict(); got.Worse(trace.VerdictSuspect) != got {
		t.Fatalf("EndVerdict = %q; a recording missing hops must rank at least suspect", got)
	}
}

func TestWireJsonlIsTheClientEdgeSubsetOfHopsJsonl(t *testing.T) {
	f := &fakeEnsemble{}
	f.push(hop(1, "edge")) // From == "" — a client call
	inner := hop(2, "bff")
	inner.From = "edge" // a provider-to-provider call
	f.push(inner)

	s := attachedSessionFor(t, f)
	if err := s.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	full, _, err := runs.ReadHops(s.Paths.HopsPath)
	if err != nil {
		t.Fatalf("ReadHops(hops.jsonl): %v", err)
	}
	if len(full) != 2 {
		t.Fatalf("hops.jsonl = %d hop(s), want the full chain of 2", len(full))
	}
	wire, _, err := runs.ReadHops(s.Paths.WirePath)
	if err != nil {
		t.Fatalf("ReadHops(wire.jsonl): %v", err)
	}
	if len(wire) != 1 || wire[0].To != "edge" || wire[0].From != "" {
		t.Fatalf("wire.jsonl = %+v, want only the From==\"\" client-edge hop", wire)
	}
}

// --- zero-value pins (global-constraints.md, both clauses) ---

// failingEnsemble serves hops but refuses to end the session — the shape of
// a control plane that died, restarted, or lost the session mid-run.
type failingEnsemble struct{ fakeEnsemble }

func (f *failingEnsemble) EndSession(context.Context, string) (EndReport, error) {
	return EndReport{}, errors.New("connection refused")
}

// The zero EndReport has Verdict "" and Hops 0, and trace.Verdict("")
// ranks EQUAL TO VerdictOK in verdictRank (a missing map key is 0). So
// "ensemble never told us how the session went" and "ensemble said the
// session was fine" would compare equal, and a run against a control plane
// that fell over mid-flow would gate as clean. EndVerdict must resolve the
// unconfirmed case to something strictly worse than ok.
//
// Mutating EndVerdict to `return s.endReport.Verdict` makes this fail.
func TestEndVerdictOnASessionEnsembleNeverConfirmedIsNotOK(t *testing.T) {
	f := &failingEnsemble{}
	f.push(hop(1, "edge"))
	s := attachedSessionFor(t, f)

	// Before Close, nothing has been confirmed at all.
	if got := s.EndVerdict(); got == trace.VerdictOK || got == "" {
		t.Fatalf("EndVerdict before Close = %q; an unconfirmed session must not rank as ok", got)
	}
	if err := s.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := s.EndVerdict(); got.Worse(trace.VerdictSuspect) != got {
		t.Fatalf("EndVerdict after a failed EndSession = %q, want at least suspect", got)
	}
	if notes := strings.Join(s.TrustNotes(), "\n"); !strings.Contains(notes, "ending the ensemble session failed") {
		t.Fatalf("TrustNotes = %q, want it to name the teardown failure", notes)
	}
	// The recording must survive a teardown error: it is already on disk
	// by the time EndSession is called, and losing it would be strictly
	// worse than recording it with a degraded verdict.
	hops, _, err := runs.ReadHops(s.Paths.HopsPath)
	if err != nil || len(hops) != 1 {
		t.Fatalf("hops.jsonl after a failed EndSession = %v (%v), want the drained hop", hops, err)
	}
}

// okButEmptyEnsemble answers DELETE with a 200 whose verdict field is
// absent — the zero value arriving over the wire rather than from a Go
// literal. It must be treated exactly like the unconfirmed case.
type okButEmptyEnsemble struct{ fakeEnsemble }

func (f *okButEmptyEnsemble) EndSession(context.Context, string) (EndReport, error) {
	return EndReport{Hops: 1}, nil
}

func TestAnEmptyVerdictFromEnsembleDoesNotRankAsOK(t *testing.T) {
	f := &okButEmptyEnsemble{}
	f.push(hop(1, "edge"))
	s := attachedSessionFor(t, f)
	if err := s.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := s.EndVerdict(); got.Worse(trace.VerdictSuspect) != got {
		t.Fatalf("EndVerdict for an empty wire verdict = %q, want at least suspect", got)
	}
}

// RETRACE_UPSTREAM_URL exists so an app with URL-bound auth (DPoP/RFC 9449
// and similar) can sign against the real service address while routing
// bytes through RETRACE_PROXY_URL — see design.md §6.1.2. Attached mode
// doesn't require an upstream (ensemble owns the forwarding), so it must
// stay conditional: present only when the caller actually configured one.
func TestAttachedSessionExposesUpstreamURLOnlyWhenConfigured(t *testing.T) {
	f := &fakeEnsemble{}

	withUpstream, err := StartAttached(Options{
		Cwd: t.TempDir(), App: "web", Flow: "checkout", Upstream: "http://localhost:4000/",
	}, f, "bff")
	if err != nil {
		t.Fatalf("StartAttached: %v", err)
	}
	t.Cleanup(func() { _ = withUpstream.Close() })
	env := map[string]string{}
	for _, kv := range withUpstream.Env() {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	if env["RETRACE_UPSTREAM_URL"] != "http://localhost:4000" {
		t.Errorf("RETRACE_UPSTREAM_URL = %q, want the configured upstream with its trailing slash trimmed", env["RETRACE_UPSTREAM_URL"])
	}

	noUpstream := attachedSessionFor(t, &fakeEnsemble{})
	for _, kv := range noUpstream.Env() {
		k, _, _ := strings.Cut(kv, "=")
		if k == "RETRACE_UPSTREAM_URL" {
			t.Fatalf("RETRACE_UPSTREAM_URL must be absent when no upstream was configured, got %q", kv)
		}
	}
}

// Drain is attached-only. A standalone session has no control plane to poll
// and must neither error nor block runFlow.
func TestDrainIsANoOpForAStandaloneSession(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()
	s, err := StartStandalone(Options{Cwd: t.TempDir(), App: "web", Flow: "checkout", Upstream: upstream.URL})
	if err != nil {
		t.Fatalf("StartStandalone: %v", err)
	}
	defer s.Close()
	if err := s.Drain(context.Background()); err != nil {
		t.Fatalf("Drain on a standalone session = %v, want nil", err)
	}
	if got := s.EndVerdict(); got != "" {
		t.Fatalf("EndVerdict standalone = %q; there is no ensemble session to have an opinion", got)
	}
}

// retrace does not own ensemble's edge listener, so it can never witness it
// dying. Reporting one would put a broken/proxy-died verdict (Task 6's most
// severe capture reason) on a run whose capture machinery was fine.
func TestAttachedSessionsNeverReportAProxyFailure(t *testing.T) {
	f := &fakeEnsemble{}
	s := attachedSessionFor(t, f)
	s.ProxyDied(errors.New("dial 127.0.0.1:0: connect: connection refused"))
	if got := s.ProxyFailure(); got != nil {
		t.Fatalf("ProxyFailure on an attached session = %+v, want nil", got)
	}
	// WatchProxy must return immediately rather than dial ensemble's edge.
	done := make(chan struct{})
	go func() { s.WatchProxy(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("WatchProxy did not return immediately for an attached session")
	}
}

// Redaction happens at capture on EVERY write path, including this one:
// ensemble redacted with its own key list, but a recording is committed and
// shared, so retrace re-applies the keys ITS config names.
func TestAttachedWritesArePushedThroughRetracesOwnRedactor(t *testing.T) {
	f := &fakeEnsemble{}
	h := hop(1, "edge")
	h.Resp = trace.Payload{Body: `{"ok":true,"token":"secret-value"}`}
	f.push(h)

	s, err := StartAttached(Options{
		Cwd: t.TempDir(), App: "web", Flow: "checkout", Redact: []string{"token"},
	}, f, "bff")
	if err != nil {
		t.Fatalf("StartAttached: %v", err)
	}
	if err := s.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	for _, path := range []string{s.Paths.HopsPath, s.Paths.WirePath} {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if strings.Contains(string(b), "secret-value") {
			t.Fatalf("%s holds the plaintext secret; every write path redacts at capture", path)
		}
	}
}

// --- Major 1: bounded calls to a wedged control plane ---

// wedgedEnsemble's StartSession blocks until ctx is canceled, simulating a
// control plane that accepted the connection and never answered — the
// shape the fix-round-1 review measured "still blocked after 5s" against.
type wedgedEnsemble struct{ fakeEnsemble }

func (w *wedgedEnsemble) StartSession(ctx context.Context, req SessionRequest) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

func TestStartAttachedIsBoundedAgainstAWedgedControlPlane(t *testing.T) {
	start := time.Now()
	_, err := StartAttached(Options{Cwd: t.TempDir(), App: "web", Flow: "checkout"}, &wedgedEnsemble{}, "bff")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("StartAttached against a wedged control plane returned nil error; want it bounded and erroring")
	}
	if elapsed > 7*time.Second {
		t.Fatalf("StartAttached took %s against a wedged control plane; StartSession must be bounded by controlTimeout (%s), not context.Background()", elapsed, controlTimeout)
	}
}

// hopsWedgedEnsemble starts normally (so Drain has a session to run
// against) but wedges every SessionHops poll — isolating Drain's own
// per-poll bound from StartAttached's.
type hopsWedgedEnsemble struct{ fakeEnsemble }

func (w *hopsWedgedEnsemble) SessionHops(ctx context.Context, id string) ([]trace.Hop, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestDrainIsBoundedAgainstAWedgedControlPlane(t *testing.T) {
	s, err := StartAttached(Options{Cwd: t.TempDir(), App: "web", Flow: "checkout"}, &hopsWedgedEnsemble{}, "bff")
	if err != nil {
		t.Fatalf("StartAttached: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	start := time.Now()
	drainErr := s.Drain(context.Background())
	elapsed := time.Since(start)
	if drainErr == nil {
		t.Fatal("Drain against a wedged control plane returned nil error; want it bounded and erroring")
	}
	if elapsed > 3*time.Second {
		t.Fatalf("Drain took %s against a wedged control plane; each poll must be bounded by its own %s deadline, not only between polls", elapsed, drainWindow)
	}
}

// --- Major 2: EndSession must run even when writing hops fails ---

func TestCloseStillEndsTheSessionWhenWritingHopsFails(t *testing.T) {
	f := &fakeEnsemble{}
	f.push(hop(1, "edge"))
	s := attachedSessionFor(t, f)
	if err := s.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	// Force writeHops to fail: point HopsPath at a directory that does not
	// exist, so os.Create returns ENOENT before Close ever reaches
	// EndSession under the old (pre-fix) ordering.
	s.Paths.HopsPath = filepath.Join(t.TempDir(), "no-such-dir", "hops.jsonl")

	err := s.Close()
	if err == nil {
		t.Fatal("Close with an unwritable HopsPath returned nil error; the write failure must be visible to the caller")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.endCalled {
		t.Fatal("EndSession was never called after writeHops failed — the session leaks on ensemble (client-edge listener + m.sessions entry, Major 2)")
	}
}

// --- Major 5, second clause: bodyLimit's non-positive normalization ---

// cmd_run.go never sets Options.MaxBody, so it is always 0 in production.
// Deleting this normalization silently drops trace.Redactor's cap (which
// reads a non-positive limit as "no cap at all") and every attached
// recording would write response bodies to disk uncapped.
func TestBodyLimitNormalizesNonPositiveToTheProxyCap(t *testing.T) {
	for _, n := range []int{0, -1, -1000} {
		if got := bodyLimit(n); got != proxy.CaptureLimit {
			t.Fatalf("bodyLimit(%d) = %d, want proxy.CaptureLimit (%d)", n, got, proxy.CaptureLimit)
		}
	}
	if got := bodyLimit(42); got != 42 {
		t.Fatalf("bodyLimit(42) = %d, want the explicit positive value passed through unchanged", got)
	}
}

// --- Minor 6: WatchProxy's attached guard, proven with a live listener ---

// TestAttachedSessionsNeverReportAProxyFailure's dial to 127.0.0.1:0 fails
// fast whether or not the guard exists, so it cannot distinguish pass from
// fail. This test stands a live listener in for ensemble's edge instead: if
// WatchProxy's attached guard were ever removed, it would try to dial that
// listener, and the dial would be observed below.
func TestWatchProxyNeverDialsEnsemblesEdgeEvenWhenSomethingIsListening(t *testing.T) {
	f := &fakeEnsemble{}
	s := attachedSessionFor(t, f)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	dialed := make(chan struct{}, 1)
	go func() {
		conn, acceptErr := ln.Accept()
		if acceptErr == nil {
			conn.Close()
			select {
			case dialed <- struct{}{}:
			default:
			}
		}
	}()
	s.ProxyURL = "http://" + ln.Addr().String()

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	s.WatchProxy(ctx)

	select {
	case <-dialed:
		t.Fatal("WatchProxy dialed ensemble's client edge; attached sessions must never probe it")
	default:
	}
}

// TestTheStackIsFingerprintedOnceAtSessionStart pins both halves: that the
// fingerprint is read at all, and that it is read at the START. Read at the
// end it would report whatever the stack became, which after a mid-run
// redeploy is not what produced the recording.
func TestTheStackIsFingerprintedOnceAtSessionStart(t *testing.T) {
	f := &fakeEnsemble{stack: &runs.Stack{Services: map[string]string{"api": "abc123"}}}
	s := attachedSessionFor(t, f)
	defer s.Close()

	if f.stackCalls != 1 {
		t.Errorf("Stack was called %d times, want exactly 1 — at session start", f.stackCalls)
	}
	got := s.Stack()
	if got == nil || got.Services["api"] != "abc123" {
		t.Errorf("Session.Stack() = %+v, want the control plane's answer", got)
	}
	if err := s.StackUnavailable(); err != nil {
		t.Errorf("StackUnavailable = %v on a session that got an answer", err)
	}
}

// TestAControlPlaneThatCannotFingerprintStillRecordsTheRun is the trade this
// makes explicit: a missing diagnostic is not worth a refused recording. The
// reason is kept so the run can say why rather than silently having no stack.
func TestAControlPlaneThatCannotFingerprintStillRecordsTheRun(t *testing.T) {
	f := &fakeEnsemble{stackErr: errors.New("status: 404 not found")}
	s := attachedSessionFor(t, f)
	defer s.Close()

	if s.Stack() != nil {
		t.Errorf("Session.Stack() = %+v after a failed fingerprint, want nil", s.Stack())
	}
	err := s.StackUnavailable()
	if err == nil {
		t.Fatal("StackUnavailable is nil after the control plane refused")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("the reason was replaced rather than carried: %v", err)
	}
}

// TestAControlPlaneWithNothingToReportIsNotAFailure separates the two
// absences. An older ensemble answers /api/status perfectly well and has no
// fingerprints in it; that is a stack with nothing to say, not an error worth
// putting on someone's stderr.
func TestAControlPlaneWithNothingToReportIsNotAFailure(t *testing.T) {
	s := attachedSessionFor(t, &fakeEnsemble{})
	defer s.Close()

	if s.Stack() != nil {
		t.Errorf("Session.Stack() = %+v, want nil", s.Stack())
	}
	if err := s.StackUnavailable(); err != nil {
		t.Errorf("StackUnavailable = %v; nothing failed", err)
	}
}
