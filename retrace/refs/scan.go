package refs

import (
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

// The accept-time secret scan. A reference bundle is committed, and a
// repository cannot be un-published — so Accept is the last moment a likely
// credential can be caught before it becomes a permanent repository fact.
// The scan runs over the run's wire.jsonl and hops.jsonl (the two hop files
// Accept carries into the bundle), AFTER capture-time redaction has already
// done its work: everything it finds is something redaction missed.
//
// Deliberately accept-time only, never on record or diff: a false positive
// (a JWT-shaped test fixture) costs one refusal with a --force escape, not a
// broken capture loop.

// SecretFinding is one likely credential found in a staged bundle.
type SecretFinding struct {
	// File is which hop file the finding is in ("wire.jsonl" or
	// "hops.jsonl"); Seq is the offending hop's sequence number, so a human
	// can find the exact record.
	File string `json:"file"`
	Seq  uint64 `json:"seq"`
	// Path locates the value inside the hop: "req.body.session_key",
	// "resp.header.x-team-token", "query.token".
	Path string `json:"path"`
	// Kind names which detector fired: "secret-key", "jwt",
	// "aws-access-key-id", or "bearer-token".
	Kind string `json:"kind"`
	// Suggestion is the command that teaches retrace about this field —
	// actionable verbatim, so the refusal never strands the operator.
	Suggestion string `json:"suggestion"`
}

// SecretScanError is the typed refusal Accept returns when the scan finds
// likely credentials and Force is off — typed so `retrace serve`'s accept
// route (and through it the review UI) can carry the findings as values
// rather than prose, exactly as AcceptResult does for its warnings.
type SecretScanError struct {
	Findings []SecretFinding
}

func (e *SecretScanError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "refusing to promote: the staged bundle carries %d likely credential(s) — a reference bundle is committed, and a secret in git cannot be taken back:\n", len(e.Findings))
	for _, f := range e.Findings {
		fmt.Fprintf(&b, "  %s (%s, %s seq %d) — %s\n", f.Path, f.Kind, f.File, f.Seq, f.Suggestion)
	}
	b.WriteString("re-record with the field redacted, or pass --force if these are fixture values you mean to commit (the manifest will record acceptedWithSecrets: true)")
	return b.String()
}

var (
	// jwtRe is the spec'd JWT shape: an eyJ-prefixed base64url segment plus
	// two more dot-separated segments — a decoded header always starts with
	// `{"` so its base64 always starts eyJ. The signature segment may be
	// empty (alg "none"), so the third segment is *, not +.
	jwtRe = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]*`)
	// awsKeyRe is an AWS access key id, anywhere in a value.
	awsKeyRe = regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`)
	// bearerRe fires on header VALUES only: "Bearer <long token>". 20+
	// non-space characters keeps "Bearer test" fixtures out of it.
	bearerRe = regexp.MustCompile(`(?i)bearer\s+\S{20,}`)
)

// redactedValue reports a value the capture pipeline already dealt with:
// the destroy sentinel or an encrypt-mode marker. Such values are exactly
// what a clean bundle is full of, and never findings.
func redactedValue(s string) bool {
	return s == trace.Redacted || strings.HasPrefix(s, trace.EncryptedPrefix)
}

// ScanForSecrets scans one run directory's hop files for likely
// credentials. A missing file is fine (standalone runs have no hops.jsonl);
// corrupt lines are skipped exactly as runs.ReadHops always skips them —
// the scan guards against secrets that ARE there, and LoadBundle already
// owns refusing bundles that cannot be read. Findings come back sorted by
// file, then sequence, then path, so the refusal reads deterministically.
func ScanForSecrets(runDir string) ([]SecretFinding, error) {
	var out []SecretFinding
	for _, name := range []string{"wire.jsonl", "hops.jsonl"} {
		hops, _, err := runs.ReadHops(filepath.Join(runDir, name))
		if err != nil {
			return nil, fmt.Errorf("scanning %s for secrets: %w", name, err)
		}
		for _, h := range hops {
			out = append(out, scanHop(name, h)...)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Seq != b.Seq {
			return a.Seq < b.Seq
		}
		return a.Path < b.Path
	})
	return out, nil
}

func scanHop(file string, h trace.Hop) []SecretFinding {
	var out []SecretFinding
	add := func(path, kind, key string) {
		out = append(out, SecretFinding{
			File: file, Seq: h.Seq, Path: path, Kind: kind,
			Suggestion: suggestionFor(path, key),
		})
	}
	scanQuery(h.Path, add)
	scanPayload("req", h.Req, add)
	scanPayload("resp", h.Resp, add)
	return out
}

// suggestionFor picks the command that fixes this finding for good. A body
// field gets the wire-rule form — `--matcher redacted` is what keeps replay
// and diff green once the field IS redacted — plus the redaction itself; a
// header or query param has no body glob to write, so the redact entry alone
// is the fix.
func suggestionFor(path, key string) string {
	if field, ok := strings.CutPrefix(path, "req.body."); ok {
		return fmt.Sprintf("add `redact: [%s]` to retrace.yaml, re-record, then `retrace ref rule --field %s --matcher redacted`", key, field)
	}
	if field, ok := strings.CutPrefix(path, "resp.body."); ok {
		return fmt.Sprintf("add `redact: [%s]` to retrace.yaml, re-record, then `retrace ref rule --field %s --matcher redacted`", key, field)
	}
	return fmt.Sprintf("add `redact: [%s]` to retrace.yaml and re-record", key)
}

// scanQuery walks the query half of Hop.Path. Secret-list keys carrying a
// non-redacted value, plus shape detectors over every value.
func scanQuery(path string, add func(path, kind, key string)) {
	_, rawQuery, found := strings.Cut(path, "?")
	if !found {
		return
	}
	q, err := url.ParseQuery(rawQuery)
	if err != nil {
		return
	}
	for k, vals := range q {
		for _, v := range vals {
			if redactedValue(v) {
				continue
			}
			at := "query." + k
			if trace.IsSecretKey(k) {
				add(at, "secret-key", k)
				continue
			}
			shapeScan(at, k, v, add)
		}
	}
}

func scanPayload(side string, p trace.Payload, add func(path, kind, key string)) {
	for k, v := range p.Headers {
		if redactedValue(v) {
			continue
		}
		at := side + ".header." + strings.ToLower(k)
		switch {
		case trace.IsSecretKey(k):
			add(at, "secret-key", strings.ToLower(k))
		case bearerRe.MatchString(v):
			add(at, "bearer-token", strings.ToLower(k))
		default:
			shapeScan(at, strings.ToLower(k), v, add)
		}
	}
	if p.Body == "" || !strings.ContainsAny(p.Body, "{[") {
		// Non-JSON bodies still get the shape detectors: a raw JWT in a
		// text/plain response is a credential wherever it sits.
		if p.Body != "" {
			shapeScan(side+".body", "", p.Body, add)
		}
		return
	}
	var v any
	if err := json.Unmarshal([]byte(p.Body), &v); err != nil {
		shapeScan(side+".body", "", p.Body, add)
		return
	}
	scanValue(side+".body", "", v, add)
}

// scanValue walks a decoded JSON value the same way the redactor's body
// walker does — objects at any depth, arrays included — flagging secret-list
// keys whose values were not redacted, and JWT/AWS shapes in any string.
func scanValue(at, key string, v any, add func(path, kind, key string)) {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			scanValue(at+"."+k, k, val, add)
		}
	case []any:
		for i, val := range t {
			scanValue(fmt.Sprintf("%s[%d]", at, i), key, val, add)
		}
	case string:
		if redactedValue(t) {
			return
		}
		if trace.IsSecretKey(key) {
			add(at, "secret-key", key)
			return
		}
		shapeScan(at, key, t, add)
	default:
		// A secret-list key holding a number/bool/null is not a credential
		// shape worth refusing over; shape detectors are string-only.
	}
}

// shapeScan runs the value-shape detectors (JWT, AWS access key id) over
// one string. Bearer is header-only and handled in scanPayload.
func shapeScan(at, key, v string, add func(path, kind, key string)) {
	switch {
	case jwtRe.MatchString(v):
		add(at, "jwt", key)
	case awsKeyRe.MatchString(v):
		add(at, "aws-access-key-id", key)
	}
}
