package trace

import (
	"strings"
	"testing"
)

func TestEncryptModeRequiresADataKey(t *testing.T) {
	_, err := NewRedactor([]KeyRule{{Key: "account_number", Mode: ModeEncrypt}}, 0, nil)
	if err == nil {
		t.Fatal("building a Redactor with an encrypt-mode rule and no data key must fail, not silently fall back")
	}
}

func TestUnknownModeIsRejected(t *testing.T) {
	if _, err := NewRedactor([]KeyRule{{Key: "x", Mode: "obfuscate"}}, 0, nil); err == nil {
		t.Fatal("an unknown mode must be a load-time error, not silently treated as destroy")
	}
}

func TestMixedModesInOnePayload(t *testing.T) {
	key := testDataKey()
	r, err := NewRedactor([]KeyRule{
		{Key: "password", Mode: ModeDestroy},
		{Key: "account_number", Mode: ModeEncrypt},
		{Key: "display_name", Mode: ModeDisplay},
	}, 0, key)
	if err != nil {
		t.Fatalf("NewRedactor: %v", err)
	}
	p := mustPayload(t, r, Payload{
		Headers: map[string]string{"password": "hunter2"},
		Body:    `{"account_number":"12345","display_name":"Ada","keep":1}`,
	})
	if p.Headers["password"] != Redacted {
		t.Fatalf("destroy-mode header not destroyed: %q", p.Headers["password"])
	}
	if strings.Contains(p.Body, "12345") {
		t.Fatalf("encrypt-mode field leaked plaintext: %s", p.Body)
	}
	if !strings.Contains(p.Body, EncryptedPrefix) {
		t.Fatalf("encrypt-mode field has no marker: %s", p.Body)
	}
	if !strings.Contains(p.Body, `"display_name":"Ada"`) {
		t.Fatalf("display-mode field was altered: %s", p.Body)
	}
	if !strings.Contains(p.Body, `"keep":1`) {
		t.Fatalf("unrelated field damaged: %s", p.Body)
	}
}

// TestCorruptedEncryptStateDegradesInsteadOfPanicking pins the 7.1 contract:
// a Redactor whose encrypt invariant broke AFTER construction (here: the
// data key corrupted underneath it) reports an error instead of panicking on
// the live record path — and the value it failed to seal comes back
// DESTROYED, never plaintext, so the failure cannot leak what it was
// protecting. DegradeHop is the recorder's follow-through: bodies dropped,
// Err naming the failure.
func TestCorruptedEncryptStateDegradesInsteadOfPanicking(t *testing.T) {
	r, err := NewRedactor([]KeyRule{
		{Key: "account_number", Mode: ModeEncrypt},
		{Key: "x-team-token", Mode: ModeEncrypt},
	}, 0, testDataKey())
	if err != nil {
		t.Fatalf("NewRedactor: %v", err)
	}
	r.dataKey = r.dataKey[:5] // the invariant NewRedactor enforced, broken

	h, herr := r.Hop(Hop{
		Req:  Payload{Headers: map[string]string{"X-Team-Token": "tok-plain"}},
		Resp: Payload{Body: `{"account_number":"12345","keep":1}`},
	})
	if herr == nil {
		t.Fatal("a corrupted encrypt state must surface as an error, not succeed")
	}
	if h.Req.Headers["X-Team-Token"] != Redacted {
		t.Fatalf("header the redactor failed to seal must come back destroyed, got %q", h.Req.Headers["X-Team-Token"])
	}
	if strings.Contains(h.Resp.Body, "12345") {
		t.Fatalf("body value the redactor failed to seal leaked plaintext: %s", h.Resp.Body)
	}

	d := DegradeHop(h, herr)
	if d.Req.Body != "" || d.Resp.Body != "" || d.Req.BodyB64 != "" || d.Resp.BodyB64 != "" {
		t.Fatalf("DegradeHop must drop every payload body: %+v", d)
	}
	if !strings.Contains(d.Err, "redaction failed") {
		t.Fatalf("DegradeHop must name the failure on Err, got %q", d.Err)
	}
}

func TestDestroyModeUnchangedForExistingConfigs(t *testing.T) {
	// A bare key list (Mode left zero-valued) must behave exactly like the
	// pre-modes Redactor: destroy, nothing more.
	r := mustRedactor(t, []KeyRule{{Key: "card_number"}}, 0)
	p := mustPayload(t, r, Payload{Body: `{"card_number":"4111"}`})
	if !strings.Contains(p.Body, Redacted) || strings.Contains(p.Body, "4111") {
		t.Fatalf("bare-mode rule did not destroy: %s", p.Body)
	}
}
