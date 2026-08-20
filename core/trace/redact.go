package trace

import (
	"encoding/json"
	"strings"
)

// Redacted is the marker written over sensitive values. It matches the
// JS prototype so ported fixtures stay valid.
const Redacted = "[redacted]"

// defaultRedactHeaders are always redacted regardless of user config —
// redaction happens at capture, never post-hoc.
var defaultRedactHeaders = []string{"authorization", "cookie", "set-cookie", "dpop"}

// Redactor scrubs sensitive headers and JSON body fields and caps body
// size. The user key list applies to both headers and body field names
// (the JS prototype semantics). maxBody <= 0 means no cap.
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

// Hop returns a copy with both payloads scrubbed.
func (r *Redactor) Hop(h Hop) Hop {
	h.Req = r.Payload(h.Req)
	h.Resp = r.Payload(h.Resp)
	return h
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
