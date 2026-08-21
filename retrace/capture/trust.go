// trust.go produces the capture-trust verdict: the single judgement of
// whether a recording can be trusted at all. Tasks 10, 11, 13 and 16 all
// read the result, and Task 10 uses raw Status (not Fatal) to quarantine a
// side of `retrace diff` — comparing against a known-broken capture produces
// confident nonsense, not "identical".
package capture

import (
	"fmt"
	"sort"
	"time"

	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

// DefaultGapThreshold is how long a stretch with no captured call has to be
// before it counts as evidence, when AssessInput.GapThreshold is unset.
//
// Sixty seconds, and the value is a judgement, so here is the judgement: a
// human-driven mobile or web flow that goes a full minute without a single
// call has almost always stopped routing through the proxy — real think
// time, animations, retries and polling all land far under it — while the
// legitimate long pauses (waiting on a push notification, an OTP, a
// third-party redirect) are exactly the ones a flow can declare with a
// `quiet` group and have subtracted. Lower and every deliberate wait
// becomes a false `suspect`; higher and a proxy that died mid-run reads as
// clean.
//
// Measured BETWEEN consecutive calls only, never against the run's own
// start/end: the app launching before its first call, and teardown after
// the last, are normal and would fire on every run.
//
// Named `Default…` to match the other zero-value fallbacks in this plan
// (`DefaultCountTolerance`, `config.DefaultGate`, `config.DefaultFine`),
// and there is exactly ONE declaration of this number.
const DefaultGapThreshold = 60 * time.Second

// AssessInput is everything Assess needs to rank the evidence for one run.
//
// RequestsSeen is -1 when the mode cannot count reachability at all
// (ensemble-attached: proxied traffic reaches ensemble's own edge listener
// and never touches retrace, so retrace can only ever count marker-door
// hits), 0 when retrace counted and nothing arrived, and >0 when something
// did. See Assess's "zero calls" branch for the ruling on how this task
// reads a >0 value — it is NOT proof real traffic flowed, only proof
// something admitted by the mux reached retrace, and that is deliberately
// never enough on its own to reach VerdictOK.
type AssessInput struct {
	ProxyConfigured     bool
	ProxyFailure        *ProxyFailure
	Hops                []trace.Hop
	Checkpoints         int
	ExpectedCheckpoints int // -1 = no history to compare against
	TestExitCode        int
	RequestsSeen        int // -1 = unknown
	Quiet               []runs.Group
	GapThreshold        time.Duration
	SessionVerdict      trace.Verdict
	SessionReasons      []string
	Notes               []string // Session.trustNotes (drain shortfall, teardown failure)
}

// Assess ranks every reason a capture might not be trustworthy and returns
// the worst one. The empty AssessInput{} — every zero value at once — must
// never rank as trace.VerdictOK: ProxyConfigured false is the only branch
// that produces no reasons at all, and even then the returned Status is
// still an explicit trace.VerdictOK, never the zero string (see
// runs.WriteManifest's rejection of an empty Capture.Status).
func Assess(in AssessInput) runs.CaptureTrust {
	threshold := in.GapThreshold
	if threshold <= 0 {
		threshold = DefaultGapThreshold
	}
	gaps := FindGaps(in.Hops, threshold, in.Quiet)
	var reasons []runs.TrustReason
	add := func(code string, st trace.Verdict, detail, hint string) {
		reasons = append(reasons, runs.TrustReason{Code: code, Status: st, Detail: detail, Hint: hint})
	}

	// The P0 flowlens shipped with: a failed test used to leave the status
	// at its 'ok' default, so a run that verified nothing read as clean.
	// `failed` outranks broken/degraded deliberately — those mean the
	// capture machinery misbehaved though the test may have passed; a failed
	// test means nothing was verified at all.
	if in.TestExitCode != 0 {
		add("test-failed", trace.VerdictFailed,
			"capture not verified — the test failed, so nothing here was checked",
			"fix the failing test and re-run; a failed test proves nothing about the capture")
	}

	// There is deliberately no `proxy-never-started` reason. A bind failure
	// aborts StartStandalone before a run directory or a manifest exists,
	// so a recording can never carry it — a reason code only its own unit
	// test can reach is a reason code that lies about coverage. The one
	// producer of a ProxyFailure is Session.ProxyDied, and it always sets
	// Phase "running".
	if in.ProxyFailure != nil {
		add("proxy-died", trace.VerdictBroken,
			"the capture listener stopped during the run: "+in.ProxyFailure.Message,
			"re-run — calls made after it stopped were never recorded")
	}

	// Zero calls is ambiguous on its own: "genuinely quiet" and "the app
	// never routed through us" look identical. RequestsSeen (markers
	// included, a strictly broader count) tells them apart; -1 means we
	// could not verify, which must say so rather than read as either.
	//
	// This is also where the two RequestsSeen hazards get resolved, by
	// construction rather than by discounting the count itself:
	//
	//   - Inflation (the mux counts the plan's own preflight probe, and a
	//     405/404/malformed-body 400): this branch is the ONLY place
	//     RequestsSeen is read, and it never promotes a zero-Hops run past
	//     VerdictDegraded. A "clean" verdict (VerdictOK) is reachable only
	//     when len(in.Hops) > 0 — i.e. real calls were recorded — so an
	//     inflated RequestsSeen can, at best, turn a `broken` verdict into a
	//     `degraded` one. It can never turn a dead proxy into an `ok` one.
	//     Pinned by TestInflatedRequestsSeenNeverReadsAsClean.
	//   - Live-proxy-looking-dead (attached mode legitimately counts zero,
	//     because proxied traffic never reaches retrace at all): the -1
	//     sentinel is a THIRD value, distinct from 0, so a healthy attached
	//     run with no calls reads as `degraded`/no-calls (unknown
	//     reachability), never `broken`/proxy-never-reached. Pinned by the
	//     "zero calls, reachability unknown" case in
	//     TestAssessRanksTheWorstEvidence.
	if in.ProxyConfigured && in.ProxyFailure == nil && len(in.Hops) == 0 && in.TestExitCode == 0 {
		switch {
		case in.RequestsSeen == 0:
			add("proxy-never-reached", trace.VerdictBroken,
				"zero calls AND zero requests of any kind reached retrace — the app almost certainly never routed through it",
				"confirm the app's base URL uses $RETRACE_PROXY_URL before trusting anything else here")
		case in.RequestsSeen > 0:
			add("no-calls", trace.VerdictDegraded,
				fmt.Sprintf("the test passed and %d request(s) reached retrace, but zero calls were recorded", in.RequestsSeen),
				"check the app's base URL actually points at $RETRACE_PROXY_URL")
		default:
			add("no-calls", trace.VerdictDegraded,
				"the test passed but zero calls were recorded, and whether retrace was reached at all could not be verified — treat this zero as unknown, not confirmed clean",
				"check the app's base URL actually points at $RETRACE_PROXY_URL")
		}
	}

	if in.ExpectedCheckpoints > 0 && in.Checkpoints == 0 && in.TestExitCode == 0 {
		add("no-screenshots", trace.VerdictDegraded,
			fmt.Sprintf("the test passed but captured no screenshots — the last good run took %d", in.ExpectedCheckpoints),
			"check the test still writes shots into $RETRACE_RUN_DIR/shots")
	}

	// ensemble already proved this one at the source (SessionManager names
	// the service that dropped baggage). Carry its reasons verbatim rather
	// than re-deriving a weaker version here.
	for _, r := range in.SessionReasons {
		add("propagation-gap", in.SessionVerdict, r,
			"make the named service forward the `baggage` header alongside `traceparent`")
	}
	for _, n := range in.Notes {
		add("capture-note", trace.VerdictSuspect, n, "re-run if the recording matters; the artifact may be incomplete")
	}

	if len(gaps) > 0 {
		longest := gaps[0]
		for _, g := range gaps {
			if g.Seconds > longest.Seconds {
				longest = g
			}
		}
		// Evidence, not a verdict: a gap cannot tell a dead proxy from an
		// idle test.
		add("quiet-stretch", trace.VerdictSuspect,
			fmt.Sprintf("%d stretch(es) of %ds+ with no calls captured — longest %ds", len(gaps), int(threshold.Seconds()), longest.Seconds),
			"if the capture was restarted mid-run, calls in that window are missing, not absent")
	}

	status := trace.VerdictOK
	for _, r := range reasons {
		status = status.Worse(r.Status)
	}
	out := runs.CaptureTrust{Status: status, Reasons: reasons, Gaps: gaps, Summary: "capture looks complete"}
	for _, r := range reasons {
		if r.Status == status {
			out.Summary, out.Hint = r.Detail, r.Hint
			break
		}
	}
	return out
}

// Fatal reports whether a verdict should stop a recording from being
// promoted or trusted. `suspect` is a heuristic that fires on plenty of
// legitimate runs, so failing on it would flood false alarms and get the
// check switched off; broken/degraded/failed mean the capture did not
// happen as intended.
//
// Fatal is deliberately narrower than what Task 10's quarantine keys on:
// quarantine reads the raw Status (excluding only VerdictOK), because
// diffing against a `suspect` reference is still comparing against a run
// nobody confirmed clean.
func Fatal(c runs.CaptureTrust) bool {
	switch c.Status {
	case trace.VerdictBroken, trace.VerdictDegraded, trace.VerdictFailed:
		return true
	}
	return false
}

// FindGaps reports stretches where nothing was recorded for longer than
// threshold, MINUS any interval a flow explicitly declared quiet. A flow
// that said "I am waiting on a push notification here" has explained its
// own silence; an undeclared hole of the same length has not, and is
// usually the app having stopped routing through us mid-run.
//
// Gap.Seconds reports the UNEXPLAINED remainder, not the wall-clock span —
// the number in the report is the number the reader has to account for.
func FindGaps(hops []trace.Hop, threshold time.Duration, quiet []runs.Group) []runs.Gap {
	if len(hops) < 2 || threshold <= 0 {
		return nil
	}
	starts := make([]time.Time, len(hops))
	for i, h := range hops {
		starts[i] = h.T.Start
	}
	sort.Slice(starts, func(i, j int) bool { return starts[i].Before(starts[j]) })

	var out []runs.Gap
	for i := 1; i < len(starts); i++ {
		from, to := starts[i-1], starts[i]
		d := to.Sub(from)
		for _, g := range quiet {
			if !g.Quiet {
				continue
			}
			d -= overlap(from, to, g.StartedAt, g.EndedAt)
		}
		if d >= threshold {
			out = append(out, runs.Gap{From: from, To: to, Seconds: int(d.Seconds())})
		}
	}
	return out
}

// overlap is the intersection of two half-open intervals, or zero.
func overlap(aStart, aEnd, bStart, bEnd time.Time) time.Duration {
	start, end := aStart, aEnd
	if bStart.After(start) {
		start = bStart
	}
	if bEnd.Before(end) {
		end = bEnd
	}
	if !end.After(start) {
		return 0
	}
	return end.Sub(start)
}
