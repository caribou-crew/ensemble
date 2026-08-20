package trace

import (
	"net/url"
	"strings"
	"testing"
)

// Semantics ported from flowlens redactHops: one user list covers both
// headers and body fields; defaults always redact auth-bearing headers.
func TestRedactDefaultHeadersCaseInsensitive(t *testing.T) {
	r := NewRedactor(nil, 0)
	p := r.Payload(Payload{Headers: map[string]string{
		"Authorization": "Bearer x",
		"COOKIE":        "sid=1",
		"Set-Cookie":    "sid=2",
		"DPoP":          "tok",
		"Accept":        "application/json",
	}})
	for _, k := range []string{"Authorization", "COOKIE", "Set-Cookie", "DPoP"} {
		if p.Headers[k] != Redacted {
			t.Fatalf("%s not redacted: %q", k, p.Headers[k])
		}
	}
	if p.Headers["Accept"] != "application/json" {
		t.Fatalf("non-sensitive header touched: %q", p.Headers["Accept"])
	}
}

func TestRedactBodyFieldsRecursively(t *testing.T) {
	r := NewRedactor([]string{"token", "pan"}, 0)
	p := r.Payload(Payload{Body: `{"token":"abc","nested":{"pan":"4111","keep":1},"list":[{"pan":"4222"}]}`})
	if strings.Contains(p.Body, "abc") || strings.Contains(p.Body, "4111") || strings.Contains(p.Body, "4222") {
		t.Fatalf("secrets survived: %s", p.Body)
	}
	if !strings.Contains(p.Body, `"keep":1`) {
		t.Fatalf("non-secret field damaged: %s", p.Body)
	}
	if !strings.Contains(p.Body, Redacted) {
		t.Fatalf("marker missing: %s", p.Body)
	}
}

func TestRedactUserHeaderListAndNonJSONBody(t *testing.T) {
	r := NewRedactor([]string{"x-api-key"}, 0)
	p := r.Payload(Payload{
		Headers: map[string]string{"X-Api-Key": "k"},
		Body:    "plain text, not json",
	})
	if p.Headers["X-Api-Key"] != Redacted {
		t.Fatal("user-listed header not redacted")
	}
	if p.Body != "plain text, not json" {
		t.Fatalf("non-JSON body altered: %q", p.Body)
	}
}

func TestBodySizeCap(t *testing.T) {
	r := NewRedactor(nil, 10)
	p := r.Payload(Payload{Body: "0123456789ABCDEF"})
	if p.Body != "0123456789" || !p.Truncated {
		t.Fatalf("cap failed: body=%q truncated=%v", p.Body, p.Truncated)
	}
	small := r.Payload(Payload{Body: "tiny"})
	if small.Body != "tiny" || small.Truncated {
		t.Fatalf("small body mangled: %+v", small)
	}
}

// TestRedactHopQueryStringValues guards final-review finding I4: query
// strings bypassed redaction entirely, so a call like
// GET /v1/accounts?api_key=sk_live_... landed the secret verbatim in the
// ring, hops.jsonl, /api/traffic, and every export. Query parameter VALUES
// whose keys match the redactor's key set (defaults + user list) must be
// scrubbed before the hop is stored; the path segment and non-matching
// query keys/values must survive untouched.
func TestRedactHopQueryStringValues(t *testing.T) {
	r := NewRedactor([]string{"token"}, 0)
	h := r.Hop(Hop{Path: "/v1/accounts?authorization=Bearer%20x&token=y&keep=1"})
	if !strings.HasPrefix(h.Path, "/v1/accounts?") {
		t.Fatalf("path base mangled: %q", h.Path)
	}
	u, err := url.Parse(h.Path)
	if err != nil {
		t.Fatalf("redacted path doesn't parse: %v (%q)", err, h.Path)
	}
	q := u.Query()
	if q.Get("authorization") != Redacted {
		t.Fatalf("authorization query value not redacted: %q", h.Path)
	}
	if q.Get("token") != Redacted {
		t.Fatalf("token query value not redacted: %q", h.Path)
	}
	if q.Get("keep") != "1" {
		t.Fatalf("non-sensitive query param damaged: %q", h.Path)
	}
}

// TestRedactHopPathWithoutQueryUntouched pins the no-query case: a bare
// path must pass through unchanged (no trailing "?" added, no parsing
// error swallowing the path).
func TestRedactHopPathWithoutQueryUntouched(t *testing.T) {
	r := NewRedactor(nil, 0)
	h := r.Hop(Hop{Path: "/v1/accounts"})
	if h.Path != "/v1/accounts" {
		t.Fatalf("path without query changed: %q", h.Path)
	}
}

func TestRedactHopBothSides(t *testing.T) {
	r := NewRedactor([]string{"token"}, 0)
	h := r.Hop(Hop{
		Req:  Payload{Headers: map[string]string{"authorization": "Bearer x"}, Body: `{"token":"abc"}`},
		Resp: Payload{Headers: map[string]string{"set-cookie": "sid=1"}},
	})
	if h.Req.Headers["authorization"] != Redacted || h.Resp.Headers["set-cookie"] != Redacted {
		t.Fatal("hop headers not redacted")
	}
	if strings.Contains(h.Req.Body, "abc") {
		t.Fatal("hop body not redacted")
	}
}
