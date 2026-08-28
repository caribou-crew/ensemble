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
	p := r.Payload(Payload{
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

func TestDestroyModeUnchangedForExistingConfigs(t *testing.T) {
	// A bare key list (Mode left zero-valued) must behave exactly like the
	// pre-modes Redactor: destroy, nothing more.
	r := mustRedactor(t, []KeyRule{{Key: "card_number"}}, 0)
	p := r.Payload(Payload{Body: `{"card_number":"4111"}`})
	if !strings.Contains(p.Body, Redacted) || strings.Contains(p.Body, "4111") {
		t.Fatalf("bare-mode rule did not destroy: %s", p.Body)
	}
}
