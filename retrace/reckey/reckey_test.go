package reckey

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadTeamKeyResolvesEnvOverKeyfile(t *testing.T) {
	dir := t.TempDir()
	writeKeyfile(t, dir, bytes32('a'))
	envKey := bytes32('b')
	t.Setenv(EnvTeamKey, hex.EncodeToString(envKey))

	got, source, err := LoadTeamKey(dir)
	if err != nil {
		t.Fatalf("LoadTeamKey: %v", err)
	}
	if source != EnvTeamKey {
		t.Fatalf("source = %q, want %q", source, EnvTeamKey)
	}
	if string(got) != string(envKey) {
		t.Fatal("env key should win over the keyfile when both are present")
	}
}

func TestLoadTeamKeyFallsBackToKeyfile(t *testing.T) {
	dir := t.TempDir()
	want := bytes32('c')
	writeKeyfile(t, dir, want)

	got, source, err := LoadTeamKey(dir)
	if err != nil {
		t.Fatalf("LoadTeamKey: %v", err)
	}
	if source != filepath.Join(dir, KeyFile) {
		t.Fatalf("source = %q, want the keyfile path", source)
	}
	if string(got) != string(want) {
		t.Fatal("keyfile bytes did not round-trip")
	}
}

func TestLoadTeamKeyErrorsWhenNeitherExists(t *testing.T) {
	_, _, err := LoadTeamKey(t.TempDir())
	if !errors.Is(err, ErrNoTeamKey) {
		t.Fatalf("err = %v, want ErrNoTeamKey", err)
	}
}

func TestLoadTeamKeyEnvAcceptsHexAndBase64(t *testing.T) {
	want := bytes32('d')
	dir := t.TempDir()

	t.Setenv(EnvTeamKey, hex.EncodeToString(want))
	gotHex, _, err := LoadTeamKey(dir)
	if err != nil || string(gotHex) != string(want) {
		t.Fatalf("hex form: got %x, err %v", gotHex, err)
	}

	t.Setenv(EnvTeamKey, base64.StdEncoding.EncodeToString(want))
	gotB64, _, err := LoadTeamKey(dir)
	if err != nil || string(gotB64) != string(want) {
		t.Fatalf("base64 form: got %x, err %v", gotB64, err)
	}
}

func TestWrapUnwrapDataKeyRoundTrips(t *testing.T) {
	teamKey := bytes32('e')
	dataKey, err := GenerateDataKey()
	if err != nil {
		t.Fatalf("GenerateDataKey: %v", err)
	}
	wrapped, err := WrapDataKey(dataKey, teamKey)
	if err != nil {
		t.Fatalf("WrapDataKey: %v", err)
	}
	got, err := UnwrapDataKey(wrapped, teamKey)
	if err != nil {
		t.Fatalf("UnwrapDataKey: %v", err)
	}
	if string(got) != string(dataKey) {
		t.Fatal("wrap/unwrap did not round-trip the data key")
	}
}

func TestUnwrapDataKeyWithWrongTeamKeyFails(t *testing.T) {
	dataKey, _ := GenerateDataKey()
	wrapped, err := WrapDataKey(dataKey, bytes32('f'))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := UnwrapDataKey(wrapped, bytes32('g')); err == nil {
		t.Fatal("unwrapping with the wrong team key must fail")
	}
}

func TestKeyIDIsStableAndEightHexChars(t *testing.T) {
	key := bytes32('h')
	id1 := KeyID(key)
	id2 := KeyID(key)
	if id1 != id2 {
		t.Fatalf("KeyID is not stable: %q vs %q", id1, id2)
	}
	if len(id1) != 8 {
		t.Fatalf("KeyID length = %d, want 8", len(id1))
	}
	if _, err := hex.DecodeString(id1); err != nil {
		t.Fatalf("KeyID is not hex: %q", id1)
	}
}

func bytes32(fill byte) []byte {
	b := make([]byte, KeySize)
	for i := range b {
		b[i] = fill
	}
	return b
}

func writeKeyfile(t *testing.T, dir string, key []byte) {
	t.Helper()
	path := filepath.Join(dir, KeyFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, key, 0o600); err != nil {
		t.Fatal(err)
	}
}
