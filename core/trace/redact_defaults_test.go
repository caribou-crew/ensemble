package trace

import (
	"strings"
	"testing"
)

// TestRedactDefaultCredentialHeaders covers the headers added beyond
// the prototype's original four: each carries a credential by definition, so
// none of them may reach the hop log verbatim with no user config at all.
func TestRedactDefaultCredentialHeaders(t *testing.T) {
	r := mustRedactor(t, nil, 0)
	for _, h := range []string{
		"Proxy-Authorization", "X-Api-Key", "api-key", "X-Auth-Token",
		"x-amz-security-token", "X-Goog-Api-Key",
	} {
		got := r.Payload(Payload{Headers: map[string]string{h: "s3cret"}})
		if got.Headers[h] != Redacted {
			t.Errorf("header %s = %q, want %q", h, got.Headers[h], Redacted)
		}
	}
}

// TestRedactDefaultQueryParams pins the query-only default list: a URL is
// a place credentials leak wholesale (hop log, HAR/curl exports, shell
// history), so these values are scrubbed with no user config.
func TestRedactDefaultQueryParams(t *testing.T) {
	r := mustRedactor(t, nil, 0)
	hop := r.Hop(Hop{Path: "/v1/things?access_token=abc&api_key=def&sig=ghi&user=alice"})

	if strings.Contains(hop.Path, "abc") || strings.Contains(hop.Path, "def") || strings.Contains(hop.Path, "ghi") {
		t.Errorf("credential query values survived redaction: %s", hop.Path)
	}
	// Non-credential params must survive — the point of the capture is the
	// dataflow, and over-redaction is not reversible.
	if !strings.Contains(hop.Path, "user=alice") {
		t.Errorf("non-credential param was redacted: %s", hop.Path)
	}
	if !strings.HasPrefix(hop.Path, "/v1/things?") {
		t.Errorf("path segment altered: %s", hop.Path)
	}
}

// TestQueryDefaultsDoNotLeakIntoBodyFields is the reason defaultRedactQuery
// is a separate list: a JSON body field named "token" is frequently the
// value being debugged (a login response), and redaction happens at
// capture with no way back.
func TestQueryDefaultsDoNotLeakIntoBodyFields(t *testing.T) {
	r := mustRedactor(t, nil, 0)
	got := r.Payload(Payload{Body: `{"token":"visible","password":"visible"}`})
	if !strings.Contains(got.Body, "visible") {
		t.Errorf("body field redacted by a query-only default: %s", got.Body)
	}
}
