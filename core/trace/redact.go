package trace

import (
	"encoding/json"
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

// Redactor scrubs sensitive headers and JSON body fields and caps body
// size. The user key list applies to both headers and body field names
// (prototype semantics). maxBody <= 0 means no cap.
type Redactor struct {
	keys    map[string]bool // lowercased; headers AND body fields
	maxBody int
}

func NewRedactor(userKeys []string, maxBody int) *Redactor {
	keys := make(map[string]bool, len(defaultRedactHeaders)+len(userKeys))
	for _, k := range defaultRedactHeaders {
		keys[k] = true
	}
	for _, k := range userKeys {
		keys[strings.ToLower(k)] = true
	}
	return &Redactor{keys: keys, maxBody: maxBody}
}

// Payload returns a scrubbed copy: matching headers replaced, matching
// JSON body fields replaced recursively, then the body size-capped.
func (r *Redactor) Payload(p Payload) Payload {
	if p.Headers != nil {
		h := make(map[string]string, len(p.Headers))
		for k, v := range p.Headers {
			if r.keys[strings.ToLower(k)] {
				v = Redacted
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
		if !r.keys[lk] && !defaultRedactQuery[lk] {
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
			if r.keys[strings.ToLower(k)] {
				t[k] = Redacted
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
