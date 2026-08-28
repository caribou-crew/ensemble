package reckey

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestInitKeyFileWritesA32ByteKeyWhenNoneResolves(t *testing.T) {
	dir := t.TempDir()
	path, err := InitKeyFile(dir)
	if err != nil {
		t.Fatalf("InitKeyFile: %v", err)
	}
	if path != filepath.Join(dir, KeyFile) {
		t.Fatalf("path = %q, want the project's keyfile path", path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the written keyfile: %v", err)
	}
	if len(b) != KeySize {
		t.Fatalf("wrote %d bytes, want %d", len(b), KeySize)
	}

	key, source, err := LoadTeamKey(dir)
	if err != nil {
		t.Fatalf("LoadTeamKey after init: %v", err)
	}
	if source != path {
		t.Fatalf("source = %q, want the keyfile just written", source)
	}
	if string(key) != string(b) {
		t.Fatal("LoadTeamKey did not resolve the exact bytes InitKeyFile wrote")
	}
}

func TestInitKeyFileRefusesWhenTheEnvVarAlreadyResolves(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvTeamKey, hex.EncodeToString(bytes32('e')))
	if _, err := InitKeyFile(dir); err == nil {
		t.Fatal("InitKeyFile succeeded with RETRACE_RECORDING_KEY already set — it must never risk orphaning data wrapped under that key")
	}
	if _, err := os.Stat(filepath.Join(dir, KeyFile)); err == nil {
		t.Fatal("InitKeyFile wrote a keyfile despite refusing")
	}
}

func TestInitKeyFileRefusesWhenAKeyfileAlreadyExists(t *testing.T) {
	dir := t.TempDir()
	original := bytes32('k')
	writeKeyfile(t, dir, original)

	if _, err := InitKeyFile(dir); err == nil {
		t.Fatal("InitKeyFile succeeded with a keyfile already present — it must never overwrite one")
	}
	got, err := os.ReadFile(filepath.Join(dir, KeyFile))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Fatal("the existing keyfile was modified despite InitKeyFile refusing")
	}
}
