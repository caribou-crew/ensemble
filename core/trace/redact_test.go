package trace

import (
	"net/url"
	"strings"
	"testing"
)

// destroyRules is a small test helper: a plain key list, all ModeDestroy —
// the shape every one of these tests used before per-key modes existed.
func destroyRules(keys ...string) []KeyRule {
	return DestroyKeys(keys)
}

func mustRedactor(t *testing.T, rules []KeyRule, maxBody int) *Redactor {
	t.Helper()
	r, err := NewRedactor(rules, maxBody, nil)
	if err != nil {
		t.Fatalf("NewRedactor: %v", err)
	}
	return r
}

// mustPayload/mustHop scrub and require success — the shape every test here
// had before redaction failures became error returns instead of panics.
func mustPayload(t *testing.T, r *Redactor, p Payload) Payload {
	t.Helper()
	out, err := r.Payload(p)
	if err != nil {
		t.Fatalf("Payload: %v", err)
	}
	return out
}

func mustHop(t *testing.T, r *Redactor, h Hop) Hop {
	t.Helper()
	out, err := r.Hop(h)
	if err != nil {
		t.Fatalf("Hop: %v", err)
	}
	return out
}

// Semantics ported from the prototype's redactHops: one user list covers both
// headers and body fields; defaults always redact auth-bearing headers.
func TestRedactDefaultHeadersCaseInsensitive(t *testing.T) {
	r := mustRedactor(t, nil, 0)
	p := mustPayload(t, r, Payload{Headers: map[string]string{
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
	r := mustRedactor(t, destroyRules("token", "pan"), 0)
	p := mustPayload(t, r, Payload{Body: `{"token":"abc","nested":{"pan":"4111","keep":1},"list":[{"pan":"4222"}]}`})
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
	r := mustRedactor(t, destroyRules("x-api-key"), 0)
	p := mustPayload(t, r, Payload{
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
	r := mustRedactor(t, nil, 10)
	p := mustPayload(t, r, Payload{Body: "0123456789ABCDEF"})
	if p.Body != "0123456789" || !p.Truncated {
		t.Fatalf("cap failed: body=%q truncated=%v", p.Body, p.Truncated)
	}
	small := mustPayload(t, r, Payload{Body: "tiny"})
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
	r := mustRedactor(t, destroyRules("token"), 0)
	h := mustHop(t, r, Hop{Path: "/v1/accounts?authorization=Bearer%20x&token=y&keep=1"})
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
	r := mustRedactor(t, nil, 0)
	h := mustHop(t, r, Hop{Path: "/v1/accounts"})
	if h.Path != "/v1/accounts" {
		t.Fatalf("path without query changed: %q", h.Path)
	}
}

func TestRedactHopBothSides(t *testing.T) {
	r := mustRedactor(t, destroyRules("token"), 0)
	h := mustHop(t, r, Hop{
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
