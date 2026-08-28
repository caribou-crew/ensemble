package trace

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// Redacted is the marker written over sensitive values. It matches the
// JS prototype so ported fixtures stay valid.
const Redacted = "[redacted]"

// defaultRedactHeaders are always redacted regardless of user config —
// redaction happens at capture, never post-hoc. Beyond the original four,
// these are the headers that carry a bearer credential by
// definition, so a capture that leaked them would leak an account: the
// proxy-auth twin of Authorization, and the API-key/session-token headers
// AWS, GCP, and most gateways use.
var defaultRedactHeaders = []string{
	"authorization", "cookie", "set-cookie", "dpop",
	"proxy-authorization", "x-api-key", "api-key", "x-auth-token",
	"x-amz-security-token", "x-goog-api-key",
}

// defaultRedactQuery names query parameters whose VALUES are always
// redacted in Hop.Path, on top of the shared key set.
//
// Kept separate from defaultRedactHeaders because the shared key set
// applies to headers AND JSON body fields: a URL carrying "?token=" is a
// credential in transit — it lands in the hop log, every export, and the
// shell history of whoever runs the curl export — while a body field named
// "token" is frequently the exact value a developer is debugging (a login
// response, a refresh flow), and redaction is irreversible.
var defaultRedactQuery = func() map[string]bool {
	m := map[string]bool{}
	for _, k := range []string{
		"access_token", "refresh_token", "id_token", "token",
		"api_key", "apikey", "client_secret",
		"signature", "x-amz-signature", "sig", "password", "pwd",
	} {
		m[k] = true
	}
	return m
}()

// Mode is how a redaction key's matched value is treated at capture.
type Mode string

const (
	// ModeDestroy replaces the value with the literal Redacted marker,
	// unrecoverable — today's only behavior, and still the default for a
	// bare-scalar redact: entry and for the built-in header/query lists.
	ModeDestroy Mode = "destroy"
	// ModeEncrypt seals the value with AES-256-GCM under the Redactor's
	// data key (see EncryptField); NewRedactor refuses to build a Redactor
	// that has an encrypt-mode rule but no data key.
	ModeEncrypt Mode = "encrypt"
	// ModeDisplay passes the value through untouched at capture. It exists
	// so a config can carve a field out of a broader glob without also
	// destroying or encrypting it — the dashboard still masks it behind a
	// reveal action, screen protection only.
	ModeDisplay Mode = "display"
)

// KeyRule is one user-configured redaction key and the mode it redacts
// under.
type KeyRule struct {
	Key  string
	Mode Mode
}

// DestroyKeys converts a plain key list into destroy-mode rules — the shape
// every caller had before per-key modes existed, kept for callers (like
// ensemble.yaml's own redact: list) that this change does not extend with
// modes.
func DestroyKeys(keys []string) []KeyRule {
	rules := make([]KeyRule, len(keys))
	for i, k := range keys {
		rules[i] = KeyRule{Key: k, Mode: ModeDestroy}
	}
	return rules
}

// Redactor scrubs sensitive headers and JSON body fields and caps body
// size. The user key list applies to both headers and body field names
// (prototype semantics). maxBody <= 0 means no cap.
type Redactor struct {
	keys    map[string]Mode // lowercased; headers AND body fields
	maxBody int
	dataKey []byte // 32-byte AES-256-GCM key; nil when no rule needs it
}

// NewRedactor builds a Redactor from rules plus the built-in header list
// (always ModeDestroy). dataKey is the per-run data key used to seal any
// ModeEncrypt field; it may be nil as long as no rule asks for ModeEncrypt.
// A rule with mode ModeEncrypt and no data key is a hard failure — a
// capture that silently wrote plaintext, or silently downgraded to
// ModeDestroy, would be a surprise a developer discovers by grepping a
// committed bundle, which is exactly the failure mode this type exists to
// prevent.
func NewRedactor(rules []KeyRule, maxBody int, dataKey []byte) (*Redactor, error) {
	if len(dataKey) != 0 && len(dataKey) != 32 {
		return nil, fmt.Errorf("trace: redact data key must be 32 bytes, got %d", len(dataKey))
	}
	keys := make(map[string]Mode, len(defaultRedactHeaders)+len(rules))
	for _, k := range defaultRedactHeaders {
		keys[k] = ModeDestroy
	}
	for _, r := range rules {
		mode := r.Mode
		if mode == "" {
			mode = ModeDestroy
		}
		switch mode {
		case ModeDestroy, ModeDisplay:
		case ModeEncrypt:
			if len(dataKey) == 0 {
				return nil, fmt.Errorf("trace: redact key %q needs mode encrypt but no data key was provided", r.Key)
			}
		default:
			return nil, fmt.Errorf("trace: redact key %q: unknown mode %q", r.Key, mode)
		}
		keys[strings.ToLower(r.Key)] = mode
	}
	return &Redactor{keys: keys, maxBody: maxBody, dataKey: dataKey}, nil
}

// apply transforms one matched value per its mode. ModeEncrypt cannot fail
// here in practice: NewRedactor already refused to build a Redactor with an
// encrypt rule and no (or malformed) data key, so a failure at this point
// means that invariant broke, not a bad input — the same "unrecoverable"
// framing this package already gives a crypto/rand failure elsewhere.
func (r *Redactor) apply(mode Mode, v string) string {
	switch mode {
	case ModeDisplay:
		return v
	case ModeEncrypt:
		marker, err := EncryptField(r.dataKey, v)
		if err != nil {
			panic("trace: " + err.Error())
		}
		return marker
	default:
		return Redacted
	}
}

// Payload returns a scrubbed copy: matching headers replaced per their
// mode, matching JSON body fields replaced recursively, then the body
// size-capped.
func (r *Redactor) Payload(p Payload) Payload {
	if p.Headers != nil {
		h := make(map[string]string, len(p.Headers))
		for k, v := range p.Headers {
			if mode, ok := r.keys[strings.ToLower(k)]; ok {
				v = r.apply(mode, v)
			}
			h[k] = v
		}
		p.Headers = h
	}
	p.Body = r.redactBody(p.Body)
	if r.maxBody > 0 && len(p.Body) > r.maxBody {
		p.Body = p.Body[:r.maxBody]
		p.Truncated = true
	}
	return p
}

// Hop returns a copy with both payloads scrubbed and query-string secrets
// in Path redacted.
func (r *Redactor) Hop(h Hop) Hop {
	h.Req = r.Payload(h.Req)
	h.Resp = r.Payload(h.Resp)
	h.Path = r.redactPath(h.Path)
	return h
}

// redactPath scrubs the VALUES of query parameters whose key matches the
// redactor's key set (same defaults + user list used for headers/body
// fields) — Path is captured as method+path+raw-query
// (r.URL.RequestURI()), and unlike headers/JSON bodies it was never
// touched by redaction, so signed URLs and API-key query params (e.g.
// "?api_key=..." or "?access_token=...") landed in the ring, the hops
// file, and every export verbatim. The path segment and non-matching
// params pass through untouched; a query string that fails to parse is
// left as-is rather than dropped.
func (r *Redactor) redactPath(path string) string {
	i := strings.IndexByte(path, '?')
	if i < 0 {
		return path
	}
	base, rawQuery := path[:i], path[i+1:]
	q, err := url.ParseQuery(rawQuery)
	if err != nil {
		return path
	}
	redacted := false
	for k, vals := range q {
		lk := strings.ToLower(k)
		// Query-string redaction stays destroy-only regardless of a key's
		// configured mode: a signed URL or API-key query param is a
		// credential in transit that lands in the hop log and every
		// export, and per-key modes exist for header/body FIELDS, not for
		// values embedded in a URL.
		_, matched := r.keys[lk]
		if !matched && !defaultRedactQuery[lk] {
			continue
		}
		for i := range vals {
			vals[i] = Redacted
		}
		q[k] = vals
		redacted = true
	}
	if !redacted {
		return path
	}
	return base + "?" + q.Encode()
}

// redactBody rewrites matching field values in a JSON body. Non-JSON
// bodies pass through untouched (header redaction still applies).
func (r *Redactor) redactBody(body string) string {
	if body == "" || !strings.ContainsAny(body, "{[") {
		return body
	}
	var v any
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		return body
	}
	v = r.redactValue(v)
	out, err := json.Marshal(v)
	if err != nil {
		return body
	}
	return string(out)
}

func (r *Redactor) redactValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if mode, ok := r.keys[strings.ToLower(k)]; ok {
				t[k] = r.applyValue(mode, val)
			} else {
				t[k] = r.redactValue(val)
			}
		}
		return t
	case []any:
		for i, val := range t {
			t[i] = r.redactValue(val)
		}
		return t
	default:
		return v
	}
}

// applyValue transforms one matched BODY field value per its mode. Unlike
// apply (headers, always strings), a JSON field can be any value —
// ModeEncrypt seals its canonical JSON encoding so a decrypt can hand the
// field back as valid JSON of whatever type it originally was.
func (r *Redactor) applyValue(mode Mode, v any) any {
	switch mode {
	case ModeDisplay:
		return v
	case ModeEncrypt:
		enc, err := json.Marshal(v)
		if err != nil {
			panic("trace: " + err.Error())
		}
		marker, err := EncryptField(r.dataKey, string(enc))
		if err != nil {
			panic("trace: " + err.Error())
		}
		return marker
	default:
		return Redacted
	}
}
