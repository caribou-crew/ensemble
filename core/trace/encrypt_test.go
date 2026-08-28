package trace

import (
	"strings"
	"testing"
)

func testDataKey() []byte {
	return []byte("01234567890123456789012345678901")[:32]
}

func TestEncryptFieldRoundTrips(t *testing.T) {
	key := testDataKey()
	marker, err := EncryptField(key, "4111111111111111")
	if err != nil {
		t.Fatalf("EncryptField: %v", err)
	}
	if !IsEncrypted(marker) {
		t.Fatalf("marker does not look encrypted: %q", marker)
	}
	if strings.Contains(marker, "4111111111111111") {
		t.Fatalf("plaintext leaked into marker: %q", marker)
	}
	got, err := DecryptField(key, marker)
	if err != nil {
		t.Fatalf("DecryptField: %v", err)
	}
	if got != "4111111111111111" {
		t.Fatalf("round trip mismatch: got %q", got)
	}
}

func TestDecryptFieldWithWrongKeyFails(t *testing.T) {
	marker, err := EncryptField(testDataKey(), "secret")
	if err != nil {
		t.Fatalf("EncryptField: %v", err)
	}
	other := []byte("other-key-other-key-other-key-32")[:32]
	if _, err := DecryptField(other, marker); err == nil {
		t.Fatal("decrypt with the wrong key should fail, not succeed")
	}
}

func TestDecryptFieldOnANonMarkerErrors(t *testing.T) {
	if _, err := DecryptField(testDataKey(), "plain value"); err == nil {
		t.Fatal("decrypting a non-marker string should error, not panic or silently succeed")
	}
}

func TestIsEncrypted(t *testing.T) {
	cases := map[string]bool{
		"hello":                    false,
		Redacted:                   false,
		"$enc:v1:AAAA":             true,
		"$enc:v1:":                 true,
		"looks$enc:v1:like a trap": false,
	}
	for s, want := range cases {
		if got := IsEncrypted(s); got != want {
			t.Errorf("IsEncrypted(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestTwoEncryptionsOfTheSameValueProduceDifferentMarkers(t *testing.T) {
	key := testDataKey()
	a, err := EncryptField(key, "same")
	if err != nil {
		t.Fatal(err)
	}
	b, err := EncryptField(key, "same")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("two encryptions of the same plaintext must not produce the same marker — the nonce must be fresh per call")
	}
}
