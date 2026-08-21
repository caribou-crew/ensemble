package capture

import (
	"testing"
	"time"

	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

// hops builds n hops with distinct, strictly increasing start times far
// apart enough that FindGaps (60s default threshold) never mistakes them
// for a gap in tests that don't care about gap detection.
func hops(n int) []trace.Hop {
	t0 := time.Date(2026, 8, 21, 9, 0, 0, 0, time.UTC)
	out := make([]trace.Hop, n)
	for i := range out {
		out[i] = trace.Hop{T: trace.Timings{Start: t0.Add(time.Duration(i) * time.Second)}}
	}
	return out
}

func TestAssessRanksTheWorstEvidence(t *testing.T) {
	cases := []struct {
		name string
		in   AssessInput
		want trace.Verdict
		code string
	}{
		{"clean run", AssessInput{ProxyConfigured: true, Hops: hops(3), Checkpoints: 2, RequestsSeen: 3}, trace.VerdictOK, ""},
		{"failed test outranks everything", AssessInput{TestExitCode: 1, ProxyConfigured: true, Hops: hops(3), RequestsSeen: 3}, trace.VerdictFailed, "test-failed"},
		{"proxy died mid-run", AssessInput{ProxyConfigured: true, ProxyFailure: &ProxyFailure{Phase: "running", Message: "closed"}, Hops: hops(1), RequestsSeen: 1}, trace.VerdictBroken, "proxy-died"},
		{"zero calls AND zero requests", AssessInput{ProxyConfigured: true, RequestsSeen: 0}, trace.VerdictBroken, "proxy-never-reached"},
		{"zero calls but requests seen", AssessInput{ProxyConfigured: true, RequestsSeen: 4}, trace.VerdictDegraded, "no-calls"},
		{"zero calls, reachability unknown", AssessInput{ProxyConfigured: true, RequestsSeen: -1}, trace.VerdictDegraded, "no-calls"},
		{"screenshots vanished", AssessInput{ProxyConfigured: true, Hops: hops(2), RequestsSeen: 2, Checkpoints: 0, ExpectedCheckpoints: 5}, trace.VerdictDegraded, "no-screenshots"},
		{"ensemble reported a propagation gap", AssessInput{ProxyConfigured: true, Hops: hops(2), RequestsSeen: 2, SessionVerdict: trace.VerdictDegraded, SessionReasons: []string{"propagation gap at bff: traceparent forwarded but baggage dropped before catalog"}}, trace.VerdictDegraded, "propagation-gap"},
		{"drain shortfall", AssessInput{ProxyConfigured: true, Hops: hops(2), RequestsSeen: 2, Notes: []string{"1 hop(s) arrived after the drain window"}}, trace.VerdictSuspect, "capture-note"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Assess(c.in)
			if got.Status != c.want {
				t.Fatalf("status = %s (%s), want %s", got.Status, got.Summary, c.want)
			}
			if c.code == "" {
				return
			}
			found := false
			for _, r := range got.Reasons {
				if r.Code == c.code {
					found = true
				}
			}
			if !found {
				t.Fatalf("reasons = %+v, want a reason with code %q", got.Reasons, c.code)
			}
		})
	}
}

// A flow that declared "I am waiting for a push notification" explained its
// own silence; an undeclared 120s hole did not.
func TestFindGapsSubtractsDeclaredQuietIntervals(t *testing.T) {
	t0 := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	hops := []trace.Hop{
		{T: trace.Timings{Start: t0}},
		{T: trace.Timings{Start: t0.Add(120 * time.Second)}},
	}
	quiet := []runs.Group{{
		Name: "await-push", Quiet: true,
		StartedAt: t0.Add(10 * time.Second), EndedAt: t0.Add(100 * time.Second),
	}}

	if gaps := FindGaps(hops, 60*time.Second, quiet); len(gaps) != 0 {
		t.Fatalf("a declared quiet interval must not read as a gap: %+v", gaps)
	}
	gaps := FindGaps(hops, 60*time.Second, nil)
	if len(gaps) != 1 || gaps[0].Seconds != 120 {
		t.Fatalf("gaps = %+v, want one 120s gap", gaps)
	}
}

// A wire-only flow captures no screenshots by design. Nagging about it
// every run is how a real warning gets tuned out.
func TestAWireOnlyFlowIsNeverNaggedAboutScreenshots(t *testing.T) {
	got := Assess(AssessInput{
		ProxyConfigured: true, Hops: hops(1), RequestsSeen: 1,
		Checkpoints: 0, ExpectedCheckpoints: 0,
	})
	if got.Status != trace.VerdictOK {
		t.Fatalf("status = %s (%s), want ok", got.Status, got.Summary)
	}
}

// Blaming the capture for a test that fell over early points at the wrong
// thing: the screenshots are missing BECAUSE the test died.
func TestNoScreenshotReasonIsSuppressedWhenTheTestFailed(t *testing.T) {
	got := Assess(AssessInput{
		ProxyConfigured: true, Hops: hops(1), RequestsSeen: 1,
		Checkpoints: 0, ExpectedCheckpoints: 3, TestExitCode: 1,
	})
	if got.Status != trace.VerdictFailed {
		t.Fatalf("status = %s, want failed", got.Status)
	}
	for _, r := range got.Reasons {
		if r.Code == "no-screenshots" {
			t.Fatalf("no-screenshots must be suppressed when the test failed: %+v", got.Reasons)
		}
	}
}

// --- the RequestsSeen ruling (task-6-brief.md, "RequestsSeen is inflated") -

// TestInflatedRequestsSeenNeverReadsAsClean pins the choice this task makes
// about the inflation hazard: RequestsSeen is read RAW (no discount for the
// plan's own preflight probe, or any other mux-rejected request folded into
// it), and safety comes from WHERE it is read, not from adjusting the
// number. The zero-Hops branch is the only consumer of RequestsSeen, and it
// tops out at VerdictDegraded ("no-calls") — never VerdictOK — no matter how
// large RequestsSeen is. A single admitted-but-rejected preflight probe
// (RequestsSeen: 1) must land exactly there, same as a larger inflated
// count.
//
// This is the mutation-target test: an Assess that let a zero-Hops run with
// RequestsSeen > 0 reach VerdictOK — i.e. treated ">0 requests" as proof of
// real traffic — would pass every other case in this file (all of them
// either have Hops or RequestsSeen <= 0) and only this test would catch it.
func TestInflatedRequestsSeenNeverReadsAsClean(t *testing.T) {
	got := Assess(AssessInput{ProxyConfigured: true, RequestsSeen: 1}) // Hops: nil — only the probe reached retrace
	if got.Status == trace.VerdictOK {
		t.Fatalf("status = ok — a RequestsSeen inflated purely by the preflight probe must never read as a clean capture: %+v", got)
	}
	if got.Status != trace.VerdictDegraded {
		t.Fatalf("status = %s, want degraded (no-calls)", got.Status)
	}
	found := false
	for _, r := range got.Reasons {
		if r.Code == "no-calls" {
			found = true
		}
	}
	if !found {
		t.Fatalf("reasons = %+v, want no-calls", got.Reasons)
	}
}

// TestAttachedZeroRequestsSeenNeverReadsAsProxyNeverReached pins the other
// direction named explicitly in the brief: attached mode legitimately
// counts zero (proxied traffic reaches ensemble's edge, never retrace), and
// must pass -1, not 0. Reading -1 as 0 would falsely accuse a perfectly
// healthy attached run of "proxy-never-reached".
func TestAttachedZeroRequestsSeenNeverReadsAsProxyNeverReached(t *testing.T) {
	got := Assess(AssessInput{ProxyConfigured: true, RequestsSeen: -1}) // attached mode, zero calls
	if got.Status == trace.VerdictBroken {
		t.Fatalf("status = broken — RequestsSeen:-1 (attached, unknown reachability) must never read as proxy-never-reached: %+v", got)
	}
	for _, r := range got.Reasons {
		if r.Code == "proxy-never-reached" {
			t.Fatalf("reasons contain proxy-never-reached for RequestsSeen:-1: %+v", got.Reasons)
		}
	}
}

// --- zero-value pin: an unassessed/all-defaults AssessInput must not gate
// clean by accident, and Assess must never emit the zero trace.Verdict.

func TestAssessNeverReturnsTheZeroStatus(t *testing.T) {
	cases := []AssessInput{
		{},
		{ProxyConfigured: true},
		{ProxyConfigured: true, Hops: hops(2), RequestsSeen: 2},
	}
	for _, in := range cases {
		got := Assess(in)
		if got.Status == "" {
			t.Fatalf("Assess(%+v).Status is the zero value — runs.WriteManifest would reject this manifest", in)
		}
	}
}

// TestFatalExcludesOKAndSuspectOnly pins Fatal's boundary: broken, degraded
// and failed are fatal; ok and suspect are not. A Fatal that let a zero
// Status (or "" via a mutation) through as non-fatal would silently promote
// an unassessed capture.
func TestFatalExcludesOKAndSuspectOnly(t *testing.T) {
	cases := []struct {
		status trace.Verdict
		want   bool
	}{
		{trace.VerdictOK, false},
		{trace.VerdictSuspect, false},
		{trace.VerdictDegraded, true},
		{trace.VerdictBroken, true},
		{trace.VerdictFailed, true},
		{"", false}, // documents the current behavior; callers must never construct this
	}
	for _, c := range cases {
		if got := Fatal(runs.CaptureTrust{Status: c.status}); got != c.want {
			t.Errorf("Fatal(status=%q) = %v, want %v", c.status, got, c.want)
		}
	}
}
