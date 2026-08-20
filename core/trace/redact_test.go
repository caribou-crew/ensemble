package trace

import (
	"strings"
	"testing"
)

// Semantics ported from the JS prototype redactHops: one user list covers both
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
