// Package replay answers a test's HTTP calls from a recorded bundle
// instead of a live stack, and re-runs a bundle against a live stack to
// see whether the recording still holds.
//
// Everything in this package fails CLOSED. A replay server is a STRICT
// mock: the entire value of replaying a recording is that a call the
// recording does not contain is reported loudly. An unmatched request
// answered with a 200 and an empty body, a zero value, or the nearest
// recorded exchange would make a test pass while proving nothing — worse
// than useless, because a green suite is then evidence of nothing at all.
// So there is no passthrough, no default response, no "close enough", and
// every zero value in this package is the refusing one.
package replay

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/retrace/diff"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

// Key identifies one recorded exchange by what a client can be asked to
// reproduce: the verb, the path, and the query. Headers are deliberately
// not part of the key — a recorded User-Agent or a fresh auth token would
// make every replay a miss, and rule-aware body matching (see Match) is
// the finer-grained tool for what genuinely identifies a call.
type Key struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	Query  string `json:"query,omitempty"`
}

// Exchange is one recorded request/response pair, lowered from a
// trace.Hop into the shape the matcher needs.
type Exchange struct {
	Key Key `json:"key"`
	// ReqBody is the recorded request body DECODED, because matching is
	// structural (a subset match, field by field, under wire rules) rather
	// than byte-wise. nil means the recording carried no PARSEABLE request
	// body — which is not the same as "no constraint": when ReqRaw is
	// non-empty the matcher falls back to comparing the recorded bytes
	// verbatim, so a form post, a protobuf or a plain-text body still
	// constrains what matches it. See requestBodyDiff.
	ReqBody any `json:"reqBody,omitempty"`
	// ReqRaw and ReqHeaders are the request side kept VERBATIM, for
	// Revalidate to re-issue the recorded call against a live stack.
	// ReqBody cannot serve that purpose: it is decoded for structural
	// matching, so re-encoding it would change key order and number
	// formatting, and a body a server signs or hashes would no longer
	// verify. Matching never reads these two; re-issuing never reads
	// ReqBody. (These are additions to the brief's Exchange — without
	// them `retrace revalidate` would have to POST an empty body, which
	// proves nothing about a write endpoint.)
	ReqRaw     string            `json:"reqRaw,omitempty"`
	ReqHeaders map[string]string `json:"reqHeaders,omitempty"`
	Status     int               `json:"status"`
	Headers    map[string]string `json:"headers,omitempty"`
	// Body is the raw recorded response body string, never re-encoded, so
	// a replayed body is byte-identical to what was recorded.
	Body string `json:"body"`
	Seq  uint64 `json:"seq"`
	// used counts how many times this exchange has already been served.
	// It is unexported because it is replay state, not bundle content:
	// marshalling it would put a runtime counter into an artifact. It is
	// what makes repeated identical calls come back in recorded order — a
	// poll-until-ready flow that got the same first response forever would
	// hang.
	used int
}

// Bundle is a loaded reference bundle: its directory, its manifest, and
// the exchanges it can answer, in recorded order.
type Bundle struct {
	Dir       string        `json:"dir"`
	Manifest  runs.Manifest `json:"manifest"`
	Exchanges []Exchange    `json:"exchanges"`
}

// LoadBundle reads a bundle directory into the exchange table a replay
// server answers from. It refuses, rather than degrades, on everything
// that would make the table quietly incomplete:
//
//   - an unreadable manifest — the bundle directory exists, so this is a
//     CORRUPT bundle, exactly as refs.Resolve treats it (Task 11);
//   - a manifest whose wire plane says it was not recorded — Counts'
//     zero value is Recorded:false, "unknown, refuse", and replaying
//     against a plane nobody recorded would answer every call with a
//     miss while looking like a strict pass of an empty contract;
//   - a wire.jsonl with corrupt lines — runs.ReadHops is deliberately
//     fail-open (a half-written record must not discard its neighbours),
//     but a dropped line here is a recorded exchange this server would
//     then report as a client deviation. That accusation must never be an
//     artifact of our own reading;
//   - zero exchanges — there is nothing to replay, and a server that
//     501s every single call is not a strict mock, it is a broken one;
//   - a TRUNCATED recorded request body — a size-capped body has no
//     trustworthy tail, so it can only be matched as a wildcard (which is
//     the F3 defect: an unparseable recorded body matching every client
//     body) or as a prefix (a wildcard with extra steps, since a client
//     that merely starts the same way is accepted). Neither is a contract,
//     so the bundle is refused instead;
//   - a TRUNCATED recorded response body — reachable by exactly the same
//     route (trace.Redactor.Hop caps BOTH payloads), and the quieter of
//     the two: truncated JSON sometimes still parses, truncated HTML or
//     text renders, and the app under test proceeds against a response the
//     upstream never sent. `retrace diff` already treats Resp.Truncated as
//     significant (diff/openapi.go's required-field check refuses to run
//     against one); a strict mock has no weaker reading available to it;
//   - a recorded response carrying Content-Encoding — the bytes in the
//     bundle are NOT what that header describes. core/proxy forwards the
//     client's Accept-Encoding verbatim and Go's transport only
//     auto-decompresses when it added that header itself, so the compressed
//     bytes are recorded into a Go string and encoding/json replaces every
//     invalid UTF-8 byte with U+FFFD when wire.jsonl is written. Replaying
//     `Content-Encoding: gzip` over that is a lie told to the client, and
//     it lands on a HIT, so the miss machinery never sees it. Stripping the
//     header would serve a mangled body as if it were fine — plausible, and
//     therefore worse. (The capture-layer root cause is out of scope here;
//     this refusal is what makes it visible instead of silent.)
func LoadBundle(dir string) (*Bundle, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("replay: bundle directory is empty — a bundle is never the process working directory")
	}
	m, err := runs.ReadManifest(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return nil, fmt.Errorf("replay: the bundle at %s cannot be read: %w — fix or delete it; replaying against a bundle whose manifest is unreadable would answer every call as a client deviation", dir, err)
	}
	if !m.Wire.Recorded {
		return nil, fmt.Errorf("replay: the bundle at %s did not record its wire plane (%s) — there is no contract here to hold a client to", dir, reasonOr(m.Wire.Reason, "no reason recorded"))
	}
	// A bundle directory is a fully-formed path the caller already resolved
	// (refs.BundleDir, which validated app/flow through the one guard body
	// in runs), so this joins a LITERAL file name onto it and re-litigates
	// nothing — see global-constraints.md on where the guard lives.
	p := filepath.Join(dir, "wire.jsonl")
	hops, skipped, err := runs.ReadHops(p)
	if err != nil {
		return nil, fmt.Errorf("replay: reading %s: %w", p, err)
	}
	if skipped > 0 {
		return nil, fmt.Errorf("replay: %s has %d unreadable line(s) — every one of them is a recorded exchange this server would then report as an unmatched call, so the accusation would be an artifact of our own reading; re-record the flow", p, skipped)
	}
	b := &Bundle{Dir: dir, Manifest: m, Exchanges: make([]Exchange, 0, len(hops))}
	for _, h := range hops {
		if err := refuse(h); err != nil {
			return nil, fmt.Errorf("replay: the bundle at %s cannot be replayed: %w", dir, err)
		}
		b.Exchanges = append(b.Exchanges, lower(h))
	}
	if len(b.Exchanges) == 0 {
		return nil, fmt.Errorf("replay: the bundle at %s records no exchanges — there is nothing to replay, and a server that answers every call with a miss is a broken mock rather than a strict one", dir)
	}
	return b, nil
}

// refuse reports the recorded hops this package will not replay. Every
// case is a recording whose BYTES no longer describe what the headers or
// the matcher would claim about them, and in each the honest answer is a
// loud refusal at load rather than a plausible answer at request time.
//
// The two truncation arms are ONE fact about the recording, not two:
// trace.Redactor.Hop runs Redactor.Payload over both payloads and the cap
// sets Truncated on whichever it shortened, so a `max_body` small enough
// to reach one payload reaches the other on the next hop. A refusal that
// covered only the request side would leave the quieter half live.
func refuse(h trace.Hop) error {
	where := strings.ToUpper(h.Method) + " " + h.Path
	if h.Req.Truncated {
		return fmt.Errorf("hop %d (%s) recorded a TRUNCATED request body — a capped body has no trustworthy tail, so it can neither be matched verbatim (every correct client would be reported as a deviation) nor treated as no constraint (every client body would match); re-record the flow with a larger capture cap", h.Seq, where)
	}
	if h.Resp.Truncated {
		return fmt.Errorf("hop %d (%s) recorded a TRUNCATED response body — the capture cut it at the size cap, so replaying it would hand the client a knowingly-short body as if it were the complete recorded response, and a short body is the QUIET failure: truncated JSON sometimes still parses and truncated text still renders, so the test proceeds against a response the upstream never sent; re-record the flow with a larger capture cap", h.Seq, where)
	}
	if enc := contentEncoding(h.Resp.Headers); enc != "" {
		return fmt.Errorf("hop %d (%s) recorded Content-Encoding: %s — the recorded body is no longer those bytes (the capture wrote them through JSON, which replaces every invalid UTF-8 byte), so replaying that header would hand the client a body it cannot decode; re-record the flow with the client sending `Accept-Encoding: identity`", h.Seq, where, enc)
	}
	return nil
}

// contentEncoding returns the recorded Content-Encoding when it claims an
// actual encoding. "identity" is the explicit no-op and is not a claim
// about the bytes, so it is not a refusal.
func contentEncoding(headers map[string]string) string {
	for k, v := range headers {
		if !strings.EqualFold(k, "content-encoding") {
			continue
		}
		if t := strings.TrimSpace(v); t != "" && !strings.EqualFold(t, "identity") {
			return t
		}
	}
	return ""
}

func reasonOr(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

// lower turns one recorded hop into an Exchange. trace.Hop.Path is a
// RequestURI (query included), so it is split with the same helper the
// wire diff uses — one splitter, so replay and diff can never disagree
// about where a path ends.
func lower(h trace.Hop) Exchange {
	path, query := diff.SplitPath(h.Path)
	return Exchange{
		Key:        Key{Method: strings.ToUpper(h.Method), Path: path, Query: query},
		ReqBody:    decodeBody(h.Req),
		ReqRaw:     h.Req.Body,
		ReqHeaders: h.Req.Headers,
		Status:     h.Status,
		Headers:    h.Resp.Headers,
		Body:       h.Resp.Body,
		Seq:        h.Seq,
	}
}

// decodeBody parses a recorded payload as JSON for structural matching.
//
// nil does NOT mean "constrains nothing" any more: Match compares the
// recorded bytes VERBATIM whenever the recording carried a request body
// this could not parse (see requestBodyDiff). A truncated payload cannot
// reach here through LoadBundle at all — the bundle is refused — so the
// Truncated branch is a belt-and-braces guard for a hand-built Exchange
// rather than a live path.
func decodeBody(p trace.Payload) any {
	if p.Truncated {
		return nil
	}
	s := strings.TrimSpace(p.Body)
	if s == "" {
		return nil
	}
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return nil
	}
	return v
}
