package diff

import (
	"encoding/hex"
	"testing"

	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/retrace/reckey"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

// TestBuildDecryptsAnEncryptedFieldWhenTheTeamKeyResolves is the
// integration-level version of TestDiffSeesARealChangeBehindTwoEncryptedMarkers:
// two real run directories, a real encryption.json sidecar each, and Build
// reading them exactly the way `retrace diff` does.
func TestBuildDecryptsAnEncryptedFieldWhenTheTeamKeyResolves(t *testing.T) {
	teamKey := bytes32(t, 'x')
	t.Setenv(reckey.EnvTeamKey, hex.EncodeToString(teamKey))

	cfg := baseConfig(t)
	dirA, dirB := t.TempDir(), t.TempDir()

	writeEncryptedSide(t, dirA, teamKey, `"1111111111111111"`)
	writeEncryptedSide(t, dirB, teamKey, `"2222222222222222"`)

	aRef := RunRef{Kind: "run", Dir: dirA, Manifest: manifest("a", nil, nil, okCapture())}
	bRef := RunRef{Kind: "run", Dir: dirB, Manifest: manifest("b", nil, nil, okCapture())}

	s := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: aRef, B: bRef, Cfg: cfg})

	if len(s.Wire.Paired) != 1 {
		t.Fatalf("expected one paired call, got %d", len(s.Wire.Paired))
	}
	if len(s.Wire.Paired[0].BodyDiff) == 0 {
		t.Fatalf("expected a body diff for the decrypted account_number field: %+v", s.Wire.Paired[0])
	}
}

// TestBuildWithNoTeamKeyComparesMarkersLiterally pins the "still-masked"
// behavior task 6.2 specifies: with no team key, a field that fails to
// decrypt is compared BYTE-FOR-BYTE, exactly like today's destroy-mode
// marker — the same literal marker on both sides is equal (no diff); two
// DIFFERENT marker byte strings are not force-equal just because retrace
// cannot see what they encrypt. (Every real EncryptField call draws a fresh
// nonce, so two independent captures of even an unchanged value will not be
// byte-identical here — that is the honest cost of comparing without a key,
// not a bug: retrace never claims two things are equal when it cannot
// actually tell.)
func TestBuildWithNoTeamKeyComparesMarkersLiterally(t *testing.T) {
	cfg := baseConfig(t)
	dirA, dirB := t.TempDir(), t.TempDir()

	// The identical byte-for-byte marker on both sides, as if the field's
	// ciphertext were literally unchanged between the two captures.
	dataKey, err := reckey.GenerateDataKey()
	if err != nil {
		t.Fatal(err)
	}
	marker, err := trace.EncryptField(dataKey, `"unchanged"`)
	if err != nil {
		t.Fatal(err)
	}
	writeWireFile(t, dirA, []trace.Hop{{Method: "GET", Path: "/checkout", Status: 200, Resp: trace.Payload{Body: `{"account_number":"` + marker + `"}`}}})
	writeWireFile(t, dirB, []trace.Hop{{Method: "GET", Path: "/checkout", Status: 200, Resp: trace.Payload{Body: `{"account_number":"` + marker + `"}`}}})
	// No encryption.json on either side (no team key, no data key needed to
	// exercise this path) and no RETRACE_RECORDING_KEY set.

	aRef := RunRef{Kind: "run", Dir: dirA, Manifest: manifest("a", nil, nil, okCapture())}
	bRef := RunRef{Kind: "run", Dir: dirB, Manifest: manifest("b", nil, nil, okCapture())}
	s := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: aRef, B: bRef, Cfg: cfg})

	if len(s.Wire.Paired) != 1 {
		t.Fatalf("expected one paired call, got %d", len(s.Wire.Paired))
	}
	if len(s.Wire.Paired[0].BodyDiff) != 0 {
		t.Fatalf("byte-identical markers on both sides must compare equal: %+v", s.Wire.Paired[0].BodyDiff)
	}
}

func writeEncryptedSide(t *testing.T, dir string, teamKey []byte, plaintextJSON string) {
	t.Helper()
	dataKey, err := reckey.GenerateDataKey()
	if err != nil {
		t.Fatal(err)
	}
	marker, err := trace.EncryptField(dataKey, plaintextJSON)
	if err != nil {
		t.Fatal(err)
	}
	writeWireFile(t, dir, []trace.Hop{{
		Method: "GET", Path: "/checkout", Status: 200,
		Resp: trace.Payload{Body: `{"account_number":"` + marker + `"}`},
	}})
	wrapped, err := reckey.WrapDataKey(dataKey, teamKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := runs.WriteEncryption(runs.Paths{RunDir: dir}, runs.Encryption{
		KeyID: reckey.KeyID(teamKey), WrappedDataKey: wrapped,
	}); err != nil {
		t.Fatal(err)
	}
}
