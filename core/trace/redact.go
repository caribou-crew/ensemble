package trace

import (
	"encoding/base64"
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

// defaultRedactBodyKeys names parameters/fields whose VALUES are secrets by
// definition: it drives both query-string redaction (Hop.Path) and the
// default JSON-body redaction, so the two can never disagree about what
// counts as a secret key. Kept separate from defaultRedactHeaders, which is
// the header-name list.
//
// Applied to bodies in DESTROY mode only — a stack that legitimately records
// fixture credentials opts out with retrace's `redact: { body_defaults: off }`
// (see SetBodyDefaults), and a user rule for the same key (e.g. mode encrypt
// or display) always wins over the default.
var defaultRedactBodyKeys = []string{
	"access_token", "refresh_token", "id_token", "token",
	"api_key", "apikey", "client_secret",
	"signature", "x-amz-signature", "sig", "password", "pwd",
}

// defaultRedactQuery is defaultRedactBodyKeys as a lookup set, for Hop.Path
// query redaction and the body walker.
var defaultRedactQuery = func() map[string]bool {
	m := map[string]bool{}
	for _, k := range defaultRedactBodyKeys {
		m[k] = true
	}
	return m
}()

// IsSecretKey reports whether a field/parameter/header NAME is on the
// built-in secret-key list (case-insensitive). Exported for retrace's
// accept-time secret scan, which must flag exactly the keys this package
// would have redacted — one list, two consumers.
func IsSecretKey(k string) bool { return defaultRedactQuery[strings.ToLower(k)] }

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
	// bodyDefaults applies defaultRedactBodyKeys (destroy mode) to JSON body
	// fields on top of the user key list. On by default — see SetBodyDefaults.
	bodyDefaults bool
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
	return &Redactor{keys: keys, maxBody: maxBody, dataKey: dataKey, bodyDefaults: true}, nil
}

// SetBodyDefaults switches the built-in body-field redaction
// (defaultRedactBodyKeys, destroy mode) on or off — retrace's
// `redact: { body_defaults: off }` opt-out, for stacks that legitimately
// record fixture credentials in bodies. Header and query-string defaults are
// unaffected: a credential in a URL lands in the hop log, every export, and
// shell history, and is never fixture data worth keeping.
func (r *Redactor) SetBodyDefaults(on bool) { r.bodyDefaults = on }

// apply transforms one matched value per its mode. ModeEncrypt should not
// fail in practice — NewRedactor already refused to build a Redactor with an
// encrypt rule and no (or malformed) data key — but a broken invariant
// surfaces as an ERROR, not a panic: this runs inside Record on the live
// path, and the recorder degrades the hop (payloads dropped, Err set) rather
// than killing the request. The value returned alongside a non-nil error is
// the DESTROYED marker, never the plaintext, so even a caller that
// mishandles the error cannot leak the value it failed to seal.
func (r *Redactor) apply(mode Mode, v string) (string, error) {
	switch mode {
	case ModeDisplay:
		return v, nil
	case ModeEncrypt:
		marker, err := EncryptField(r.dataKey, v)
		if err != nil {
			return Redacted, err
		}
		return marker, nil
	default:
		return Redacted, nil
	}
}

// Payload returns a scrubbed copy: matching headers replaced per their
// mode, matching JSON body fields replaced recursively, then the body
// size-capped. On error the returned Payload is still fully scrubbed — a
// value that could not be sealed was destroyed instead (see apply) — and the
// FIRST failure is reported; the caller decides how loudly to degrade.
func (r *Redactor) Payload(p Payload) (Payload, error) {
	var firstErr error
	if p.Headers != nil {
		h := make(map[string]string, len(p.Headers))
		for k, v := range p.Headers {
			if mode, ok := r.keys[strings.ToLower(k)]; ok {
				red, err := r.apply(mode, v)
				if err != nil && firstErr == nil {
					firstErr = err
				}
				v = red
			}
			h[k] = v
		}
		p.Headers = h
	}
	// SetCookies carries the same values as the set-cookie header, one per
	// cookie — it must be scrubbed under the same key (always present in
	// r.keys via defaultRedactHeaders, whatever mode a user configured), or
	// the ordered list would leak exactly what the joined header redacts.
	if len(p.SetCookies) > 0 {
		if mode, ok := r.keys["set-cookie"]; ok {
			cookies := make([]string, len(p.SetCookies))
			for i, v := range p.SetCookies {
				red, err := r.apply(mode, v)
				if err != nil && firstErr == nil {
					firstErr = err
				}
				cookies[i] = red
			}
			p.SetCookies = cookies
		}
	}
	body, err := r.redactBody(p.Body)
	if err != nil && firstErr == nil {
		firstErr = err
	}
	p.Body = body
	if r.maxBody > 0 && len(p.Body) > r.maxBody {
		p.Body = p.Body[:r.maxBody]
		p.Truncated = true
	}
	// A base64 body (binary capture) is never walked — it is not JSON — but
	// the size cap still applies to the bytes it encodes: truncating the
	// base64 STRING mid-quantum would corrupt every byte after the cut, so
	// the cap decodes, cuts, and re-encodes. An undecodable value is left
	// alone; downstream consumers refuse it on their own terms.
	if r.maxBody > 0 && len(p.BodyB64) > base64.StdEncoding.EncodedLen(r.maxBody) {
		if raw, err := base64.StdEncoding.DecodeString(p.BodyB64); err == nil && len(raw) > r.maxBody {
			p.BodyB64 = base64.StdEncoding.EncodeToString(raw[:r.maxBody])
			p.Truncated = true
		}
	}
	return p, firstErr
}

// Hop returns a copy with both payloads scrubbed and query-string secrets
// in Path redacted. A non-nil error reports the first redaction failure;
// the returned Hop is still scrubbed (failed values destroyed, see apply)
// and safe to degrade with DegradeHop.
func (r *Redactor) Hop(h Hop) (Hop, error) {
	req, reqErr := r.Payload(h.Req)
	resp, respErr := r.Payload(h.Resp)
	h.Req, h.Resp = req, resp
	h.Path = r.redactPath(h.Path)
	if reqErr != nil {
		return h, reqErr
	}
	return h, respErr
}

// DegradeHop is the fail-closed shape a recorder stores when redaction
// fails: both payload bodies dropped, the failure named on Err. The hop
// survives — the request that produced it is never killed — but nothing a
// failed redaction might have left behind reaches the ring or the disk.
func DegradeHop(h Hop, err error) Hop {
	h.Req.Body, h.Req.BodyB64 = "", ""
	h.Resp.Body, h.Resp.BodyB64 = "", ""
	note := "redaction failed: " + err.Error() + "; payload bodies dropped"
	if h.Err != "" {
		h.Err += "; " + note
	} else {
		h.Err = note
	}
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
// bodies pass through untouched (header redaction still applies; the
// accept-time secret scan is retrace's net for those).
func (r *Redactor) redactBody(body string) (string, error) {
	if body == "" || !strings.ContainsAny(body, "{[") {
		return body, nil
	}
	var v any
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		return body, nil
	}
	v, rerr := r.redactValue(v)
	out, err := json.Marshal(v)
	if err != nil {
		return body, rerr
	}
	return string(out), rerr
}

// redactValue walks a decoded JSON value: user keys first (their configured
// mode, including a display carve-out), then — when bodyDefaults is on — the
// built-in secret-key list at destroy mode; arrays and nested objects at any
// depth. Like Payload, it scrubs EVERYTHING it can and reports the first
// failure rather than stopping at it.
func (r *Redactor) redactValue(v any) (any, error) {
	var firstErr error
	keep := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			mode, ok := r.keys[strings.ToLower(k)]
			if !ok && r.bodyDefaults && defaultRedactQuery[strings.ToLower(k)] {
				mode, ok = ModeDestroy, true
			}
			if ok {
				red, err := r.applyValue(mode, val)
				keep(err)
				t[k] = red
			} else {
				red, err := r.redactValue(val)
				keep(err)
				t[k] = red
			}
		}
		return t, firstErr
	case []any:
		for i, val := range t {
			red, err := r.redactValue(val)
			keep(err)
			t[i] = red
		}
		return t, firstErr
	default:
		return v, nil
	}
}

// applyValue transforms one matched BODY field value per its mode. Unlike
// apply (headers, always strings), a JSON field can be any value —
// ModeEncrypt seals its canonical JSON encoding so a decrypt can hand the
// field back as valid JSON of whatever type it originally was. Failure
// follows apply's contract: the value comes back DESTROYED alongside the
// error, never plaintext.
func (r *Redactor) applyValue(mode Mode, v any) (any, error) {
	switch mode {
	case ModeDisplay:
		return v, nil
	case ModeEncrypt:
		enc, err := json.Marshal(v)
		if err != nil {
			return Redacted, err
		}
		marker, err := EncryptField(r.dataKey, string(enc))
		if err != nil {
			return Redacted, err
		}
		return marker, nil
	default:
		return Redacted, nil
	}
}
