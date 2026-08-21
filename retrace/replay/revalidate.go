package replay

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/retrace/diff"
)

// Verdicts for a revalidation. There is deliberately no "" case that means
// clean: an unset verdict is a report nobody produced, and ExitCode maps it
// to 3 (could not evaluate) rather than 0.
const (
	VerdictClean  = "clean"
	VerdictDrift  = "drift"
	VerdictFailed = "failed"
)

// maxLiveBody caps what one live response is read into for comparison. It
// matches the recording side's own capture limit closely enough that a
// body too large to record is also a body too large to compare.
const maxLiveBody = 8 << 20

// liveCallTimeout bounds ONE re-issued call. A live stack that accepts the
// connection and never answers would otherwise hang `retrace revalidate`
// forever, which is the one outcome a 0/1/2/3 contract cannot express and
// the one a CI job cannot tell apart from a slow build. It is a var, not a
// const, only so the timeout itself can be pinned by a test in reasonable
// wall-clock time.
var liveCallTimeout = 30 * time.Second

// liveClient is what re-issues a recorded call. It deliberately does NOT
// follow redirects: revalidate reports what the live stack does with the
// recorded request, and a 301/302 is exactly that — the route has moved,
// which is drift. Following it would compare the recording against the
// REDIRECT TARGET's response and report "no drift" about a call that no
// longer exists, and http.DefaultClient's redirect handling also downgrades
// POST to GET on 301/302/303, so the write endpoint would never be
// exercised at all.
var liveClient = &http.Client{
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// StatusDrift is nil when the status did not change.
type StatusDrift struct {
	Recorded int `json:"recorded"`
	Live     int `json:"live"`
}

// Drift is one recorded call whose live answer no longer matches.
type Drift struct {
	Method string           `json:"method"`
	Path   string           `json:"path"`
	Status *StatusDrift     `json:"status,omitempty"`
	Fields []diff.FieldDiff `json:"fields,omitempty"`
}

// RevalReport is what `retrace revalidate` reports and exits on.
type RevalReport struct {
	Flow    string  `json:"flow"`
	Checked int     `json:"checked"`
	Drifts  []Drift `json:"drifts"`
	Verdict string  `json:"verdict"`
}

// ExitCode maps a report onto the CI contract. The DEFAULT is 3, not 0: a
// verdict this package never set — the zero value, or one from a newer
// build this one does not understand — means "could not evaluate", and a
// pipeline must never read it as "nothing changed". (diff.ExitCode
// defaults to 0 for its own historical reasons; this is the newer rule
// from global-constraints.md and it is applied here.)
func ExitCode(r RevalReport) int {
	switch r.Verdict {
	case VerdictClean:
		return 0
	case VerdictDrift:
		return 1
	case VerdictFailed:
		return 2
	}
	return 3
}

// Revalidate re-issues every recorded request against a live upstream and
// diffs the responses with the SAME rules the wire diff uses, so a
// rule-matched volatile field is not drift. It is how a stale recording is
// detected before it starts failing replays for the wrong reason.
//
// Anything that stops it from evaluating — no bundle, no upstream, an
// upstream that will not answer — is an ERROR, never a clean report.
func Revalidate(ctx context.Context, b *Bundle, upstream string, o Options) (RevalReport, error) {
	if b == nil {
		return RevalReport{}, fmt.Errorf("revalidate: no bundle loaded — there is nothing to revalidate")
	}
	base := strings.TrimRight(strings.TrimSpace(upstream), "/")
	if base == "" {
		return RevalReport{}, fmt.Errorf("revalidate: --upstream is required — name the live stack to check the recording against")
	}
	rep := RevalReport{Flow: b.Manifest.Flow, Drifts: []Drift{}}
	gated := false

	for i := range b.Exchanges {
		e := b.Exchanges[i]
		status, body, err := issue(ctx, base, e)
		if err != nil {
			return RevalReport{}, fmt.Errorf("revalidate: %s %s against %s: %w", e.Key.Method, e.Key.Path, base, err)
		}
		rep.Checked++

		d := Drift{Method: e.Key.Method, Path: e.Key.Path}
		if status != e.Status {
			d.Status = &StatusDrift{Recorded: e.Status, Live: status}
		}
		// An unexpected >=400 is a hard gate: the live stack is not merely
		// shaped differently, it is refusing the call the recording made.
		// "Unexpected" means the recording did not already see a >=400 —
		// a recorded 404 that is still a 404 is the contract holding.
		if status >= 400 && e.Status < 400 {
			gated = true
		}
		d.Fields = fieldDrift(e, status, body, o)
		if d.Status != nil || len(d.Fields) > 0 {
			rep.Drifts = append(rep.Drifts, d)
		}
	}

	switch {
	case gated:
		rep.Verdict = VerdictFailed
	case len(rep.Drifts) > 0:
		rep.Verdict = VerdictDrift
	default:
		rep.Verdict = VerdictClean
	}
	return rep, nil
}

// issue sends one recorded request to the live stack, under its own
// deadline so one unanswering endpoint cannot hang the whole command.
func issue(ctx context.Context, base string, e Exchange) (int, string, error) {
	ctx, cancel := context.WithTimeout(ctx, liveCallTimeout)
	defer cancel()
	url := base + e.Key.Path
	if e.Key.Query != "" {
		url += "?" + e.Key.Query
	}
	var body io.Reader
	if e.ReqRaw != "" {
		body = strings.NewReader(e.ReqRaw)
	}
	req, err := http.NewRequestWithContext(ctx, e.Key.Method, url, body)
	if err != nil {
		return 0, "", err
	}
	for k, v := range e.ReqHeaders {
		if !sendableRequestHeader(k, v) {
			continue
		}
		req.Header.Set(k, v)
	}
	resp, err := liveClient.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxLiveBody))
	if err != nil {
		return 0, "", err
	}
	return resp.StatusCode, string(raw), nil
}

// sendableRequestHeader decides which recorded request headers are put
// back on the wire.
//
//   - Hop-by-hop and length headers describe the ORIGINAL connection;
//     net/http owns them on this one.
//   - A REDACTED value is not a credential. Sending the literal
//     "[redacted]" would earn a 401 from the live stack, which revalidate
//     would then report as drift the recording never caused — a false
//     accusation manufactured by our own redaction. Dropping the header
//     can still produce a 401, but that one is honest: it says the
//     recorded call cannot be reproduced without the secret, which is the
//     truth.
func sendableRequestHeader(name, value string) bool {
	if connectionHeader(name) || strings.EqualFold(name, "host") {
		return false
	}
	return value != trace.Redacted
}

// fieldDrift diffs the recorded response against the live one through
// diff.DiffWire — the same engine, and therefore the same wire rules, the
// `retrace diff` wire plane uses. Reimplementing a second body comparison
// here is how the two would come to disagree about what a rule means.
//
// Only response bodies are put on the hops: the REQUEST was re-issued from
// the recording verbatim, so a request diff could only ever be empty, and
// Drift has nowhere to report header movement (that is `retrace diff`'s
// job, over two runs, where a Date header has two real values to compare).
//
// Tolerated and Ignored outcomes are excluded by construction — DiffWire
// files them under BodyTolerated/BodyIgnored, and only BodyDiff (a real
// change) and BodyViolations (a rule that could not be satisfied) are
// drift.
func fieldDrift(e Exchange, liveStatus int, liveBody string, o Options) []diff.FieldDiff {
	path := e.Key.Path
	if e.Key.Query != "" {
		path += "?" + e.Key.Query
	}
	recorded := trace.Hop{
		Schema: trace.SchemaVersion, Seq: e.Seq, Method: e.Key.Method, Path: path,
		Status: e.Status, Resp: trace.Payload{Body: e.Body},
	}
	live := trace.Hop{
		Schema: trace.SchemaVersion, Seq: e.Seq, Method: e.Key.Method, Path: path,
		Status: liveStatus, Resp: trace.Payload{Body: liveBody},
	}
	w := diff.DiffWire([]trace.Hop{recorded}, []trace.Hop{live}, diff.Options{
		Rules:     o.Rules,
		Normalize: o.Normalize,
	})
	if len(w.Paired) == 0 {
		return nil
	}
	out := append([]diff.FieldDiff(nil), w.Paired[0].BodyDiff...)
	return append(out, w.Paired[0].BodyViolations...)
}
