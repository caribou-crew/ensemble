package runs

import (
	"os"
	"testing"

	"github.com/caribou-crew/ensemble/core/trace"
)

func TestReadEncryptionOnAnUnencryptedRunIsNilNil(t *testing.T) {
	p := newRun(t, runIDAt(fixedNow))
	got, err := ReadEncryption(p)
	if err != nil {
		t.Fatalf("ReadEncryption on a run with no encryption.json: %v", err)
	}
	if got != nil {
		t.Fatalf("ReadEncryption = %+v, want nil for a run with nothing encrypted", got)
	}
}

func TestWriteEncryptionThenReadRoundTrips(t *testing.T) {
	p := newRun(t, runIDAt(fixedNow))
	want := Encryption{KeyID: "a1b2c3d4", WrappedDataKey: "$enc:v1:AAAABBBBCCCC"}
	if err := WriteEncryption(p, want); err != nil {
		t.Fatalf("WriteEncryption: %v", err)
	}
	got, err := ReadEncryption(p)
	if err != nil {
		t.Fatalf("ReadEncryption: %v", err)
	}
	if got == nil {
		t.Fatal("ReadEncryption returned nil after WriteEncryption")
	}
	if got.Schema != EncryptionSchema {
		t.Errorf("schema = %q, want %q", got.Schema, EncryptionSchema)
	}
	if got.KeyID != want.KeyID || got.WrappedDataKey != want.WrappedDataKey {
		t.Errorf("round-trip mismatch: got %+v, want %+v", *got, want)
	}
}

func TestWriteEncryptionRejectsEmptyFields(t *testing.T) {
	p := newRun(t, runIDAt(fixedNow))
	if err := WriteEncryption(p, Encryption{WrappedDataKey: "x"}); err == nil {
		t.Fatal("WriteEncryption accepted an empty keyId")
	}
	if err := WriteEncryption(p, Encryption{KeyID: "x"}); err == nil {
		t.Fatal("WriteEncryption accepted an empty wrappedDataKey")
	}
	if _, statErr := os.Stat(p.EncryptionPath()); statErr == nil {
		t.Error("a rejected WriteEncryption still wrote encryption.json")
	}
}

func TestReadEncryptionRejectsMismatchedSchema(t *testing.T) {
	p := newRun(t, runIDAt(fixedNow))
	if err := writeJSONFile(p.encryptionPath(), map[string]string{"schema": "not-a-real-schema", "keyId": "x", "wrappedDataKey": "y"}); err != nil {
		t.Fatalf("writeJSONFile: %v", err)
	}
	if _, err := ReadEncryption(p); err == nil {
		t.Fatal("ReadEncryption accepted an encryption.json with the wrong schema")
	}
}

func TestReadManifestIsUnawareOfEncryption(t *testing.T) {
	p := newRun(t, runIDAt(fixedNow))
	m := &Manifest{
		App: "shop", Flow: "checkout", RunID: p.RunDir, StartedAt: fixedNow, FinishedAt: fixedNow,
		Capture: CaptureTrust{Status: trace.VerdictOK, Summary: "ok"},
		Wire:    Counts{Recorded: true},
	}
	if err := WriteManifest(p, m); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}
	if err := WriteEncryption(p, Encryption{KeyID: "a1b2c3d4", WrappedDataKey: "$enc:v1:AAAA"}); err != nil {
		t.Fatalf("WriteEncryption: %v", err)
	}
	got, err := ReadManifest(p.ManifestPath)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if got.App != "shop" || got.Flow != "checkout" {
		t.Fatalf("ReadManifest returned unexpected content after a sibling encryption.json was written: %+v", got)
	}
}
