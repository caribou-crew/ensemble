package trace

import (
	"fmt"
	"strings"
	"testing"
)

func TestDecryptBodyPreservesExactNumbers(t *testing.T) {
	key := []byte(strings.Repeat("k", 32))
	sealed, err := EncryptField(key, `{"id":9007199254740993,"ratio":0.1234567890123456789}`)
	if err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"id":18446744073709551615,"secret":%q}`, sealed)
	out, ok := DecryptBody(body, key)
	if !ok {
		t.Fatalf("could not decrypt body: %s", out)
	}
	for _, exact := range []string{`"id":18446744073709551615`, `"id":9007199254740993`, `"ratio":0.1234567890123456789`} {
		if !strings.Contains(out, exact) {
			t.Errorf("decryption changed JSON number: want %s in %s", exact, out)
		}
	}
}

func TestDecryptBodyLeavesTrailingNonJSONDataUntouched(t *testing.T) {
	key := []byte(strings.Repeat("k", 32))
	sealed, err := EncryptField(key, `"fixture"`)
	if err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{" {}", " trailing"} {
		body := fmt.Sprintf(`{"secret":%q}`, sealed) + suffix
		out, ok := DecryptBody(body, key)
		if !ok || out != body {
			t.Errorf("incomplete JSON parsing changed non-JSON body: (%q, %v)", out, ok)
		}
	}
}
