package diff

import (
	"strings"
	"testing"

	"github.com/caribou-crew/ensemble/core/trace"
)

func mustEncrypt(t *testing.T, key []byte, plaintext string) string {
	t.Helper()
	enc, err := trace.EncryptField(key, plaintext)
	if err != nil {
		t.Fatalf("EncryptField: %v", err)
	}
	return enc
}

func TestDecryptBodyRecoversAnEncryptedField(t *testing.T) {
	key := bytes32(t, 'a')
	marker := mustEncrypt(t, key, `"4111111111111111"`)
	body := `{"account_number":"` + marker + `"}`
	got := decryptBody(body, key)
	if strings.Contains(got, trace.EncryptedPrefix) {
		t.Fatalf("marker survived decryption: %s", got)
	}
	if !strings.Contains(got, "4111111111111111") {
		t.Fatalf("plaintext missing: %s", got)
	}
}

func TestDecryptBodyWithNoDataKeyIsANoOp(t *testing.T) {
	key := bytes32(t, 'b')
	marker := mustEncrypt(t, key, `"secret"`)
	body := `{"field":"` + marker + `"}`
	got := decryptBody(body, nil)
	if got != body {
		t.Fatalf("body changed with a nil data key: got %q, want unchanged %q", got, body)
	}
}

func TestDecryptBodyWithWrongKeyLeavesMarkerIntact(t *testing.T) {
	right := bytes32(t, 'c')
	wrong := bytes32(t, 'd')
	marker := mustEncrypt(t, right, `"secret"`)
	body := `{"field":"` + marker + `"}`
	got := decryptBody(body, wrong)
	if !strings.Contains(got, trace.EncryptedPrefix) {
		t.Fatalf("expected the marker to survive a wrong-key decrypt attempt: %s", got)
	}
}

// TestDiffSeesARealChangeBehindTwoEncryptedMarkers proves the marker alone
// would have hidden a real value change: two DIFFERENT plaintexts, sealed
// under the SAME data key, decrypt to different values and therefore
// compare as changed — not as two byte-identical markers comparing equal.
func TestDiffSeesARealChangeBehindTwoEncryptedMarkers(t *testing.T) {
	key := bytes32(t, 'e')
	hopA := trace.Hop{Method: "GET", Path: "/checkout", Resp: trace.Payload{Body: `{"account_number":"` + mustEncrypt(t, key, `"1111"`) + `"}`}}
	hopB := trace.Hop{Method: "GET", Path: "/checkout", Resp: trace.Payload{Body: `{"account_number":"` + mustEncrypt(t, key, `"2222"`) + `"}`}}

	decA := decryptHops([]trace.Hop{hopA}, key)
	decB := decryptHops([]trace.Hop{hopB}, key)

	if decA[0].Resp.Body == decB[0].Resp.Body {
		t.Fatal("two different plaintexts behind two encrypted markers must not compare equal after decryption")
	}
	if strings.Contains(decA[0].Resp.Body, trace.EncryptedPrefix) || strings.Contains(decB[0].Resp.Body, trace.EncryptedPrefix) {
		t.Fatal("marker should be gone once the data key resolves")
	}

	w := DiffWire(decA, decB, Options{})
	if len(w.Paired) != 1 {
		t.Fatalf("expected one paired call, got %d", len(w.Paired))
	}
	if len(w.Paired[0].BodyDiff) == 0 {
		t.Fatal("expected the pair to report a body diff for the decrypted account_number field")
	}
}

func bytes32(t *testing.T, fill byte) []byte {
	t.Helper()
	b := make([]byte, 32)
	for i := range b {
		b[i] = fill
	}
	return b
}
