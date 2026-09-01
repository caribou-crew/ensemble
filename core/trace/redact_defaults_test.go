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
		got := mustPayload(t, r, Payload{Headers: map[string]string{h: "s3cret"}})
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
	hop := mustHop(t, r, Hop{Path: "/v1/things?access_token=abc&api_key=def&sig=ghi&user=alice"})

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

// TestBodyDefaultsRedactSecretFields pins the audit-hardening reversal of
// the old "query defaults never touch bodies" stance: the shared secret-key
// list now applies to JSON body fields by default, at any depth, arrays
// included, in destroy mode — a login response's access_token must not reach
// a committed bundle verbatim with no user config at all.
func TestBodyDefaultsRedactSecretFields(t *testing.T) {
	r := mustRedactor(t, nil, 0)
	got := mustPayload(t, r, Payload{
		Body: `{"user":"a","access_token":"tok123","data":{"credentials":{"password":"p"}},"items":[{"client_secret":"s1"}]}`,
	})
	for _, leaked := range []string{"tok123", `"p"`, "s1"} {
		if strings.Contains(got.Body, leaked) {
			t.Errorf("secret body value survived default redaction: %s", got.Body)
		}
	}
	if !strings.Contains(got.Body, `"user":"a"`) {
		t.Errorf("non-secret field damaged: %s", got.Body)
	}
}

// TestBodyDefaultsRespectUserRulesAndOptOut: a user rule for the same key
// wins over the built-in destroy default (here a display carve-out), and
// SetBodyDefaults(false) — retrace's `redact: { body_defaults: off }` —
// restores verbatim bodies while header/query defaults keep applying.
func TestBodyDefaultsRespectUserRulesAndOptOut(t *testing.T) {
	r := mustRedactor(t, []KeyRule{{Key: "token", Mode: ModeDisplay}}, 0)
	got := mustPayload(t, r, Payload{Body: `{"token":"visible","password":"gone"}`})
	if !strings.Contains(got.Body, `"token":"visible"`) {
		t.Errorf("display-mode user rule lost to the body default: %s", got.Body)
	}
	if strings.Contains(got.Body, "gone") {
		t.Errorf("unruled secret key not destroyed by the default: %s", got.Body)
	}

	off := mustRedactor(t, nil, 0)
	off.SetBodyDefaults(false)
	verbatim := mustPayload(t, off, Payload{
		Headers: map[string]string{"Authorization": "Bearer x"},
		Body:    `{"password":"visible"}`,
	})
	if !strings.Contains(verbatim.Body, "visible") {
		t.Errorf("opt-out did not restore verbatim bodies: %s", verbatim.Body)
	}
	h := mustHop(t, off, Hop{Path: "/login?access_token=abc"})
	if strings.Contains(h.Path, "abc") || verbatim.Headers["Authorization"] != Redacted {
		t.Error("body opt-out must not switch off header/query defaults")
	}
}

// TestNonJSONBodyLeftAloneByDefaults: the walker only rewrites bodies that
// parse as JSON — a form post or plain text passes through untouched (the
// accept-time secret scan is the net for those).
func TestNonJSONBodyLeftAloneByDefaults(t *testing.T) {
	r := mustRedactor(t, nil, 0)
	got := mustPayload(t, r, Payload{Body: "password=hunter2&keep=1"})
	if got.Body != "password=hunter2&keep=1" {
		t.Errorf("non-JSON body altered by the body defaults: %q", got.Body)
	}
}
