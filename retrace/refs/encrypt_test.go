package refs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/caribou-crew/ensemble/retrace/reckey"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

// TestAcceptCopiesTheEncryptionSidecarByteForByte is task 7's D7: a
// promoted reference bundle carries encryption.json untouched, so its
// encrypted fields decrypt with whatever team key `retrace rekey` has kept
// the wrapped DEK current against — exactly like a run's.
func TestAcceptCopiesTheEncryptionSidecarByteForByte(t *testing.T) {
	cwd := t.TempDir()
	root, runID := acceptFixture(t, cwd)

	p, err := runs.PathsFor(root, "web", "checkout", runID)
	if err != nil {
		t.Fatal(err)
	}
	teamKey := make([]byte, 32)
	for i := range teamKey {
		teamKey[i] = 'k'
	}
	dataKey, err := reckey.GenerateDataKey()
	if err != nil {
		t.Fatal(err)
	}
	wrapped, err := reckey.WrapDataKey(dataKey, teamKey)
	if err != nil {
		t.Fatal(err)
	}
	want := runs.Encryption{KeyID: reckey.KeyID(teamKey), WrappedDataKey: wrapped}
	if err := runs.WriteEncryption(p, want); err != nil {
		t.Fatal(err)
	}

	res, err := Accept(acceptOpts(cwd, root, runID))
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}

	if _, err := os.Stat(filepath.Join(res.Dir, runs.EncryptionFile)); err != nil {
		t.Fatalf("encryption.json missing from the promoted bundle: %v", err)
	}
	got, err := runs.ReadEncryption(runs.Paths{RunDir: res.Dir})
	if err != nil {
		t.Fatalf("ReadEncryption(bundle): %v", err)
	}
	if got == nil {
		t.Fatal("ReadEncryption(bundle) = nil, want the copied sidecar")
	}
	if got.KeyID != want.KeyID || got.WrappedDataKey != want.WrappedDataKey {
		t.Fatalf("copied sidecar = %+v, want %+v — byte-for-byte, not re-wrapped", *got, want)
	}

	// The data key the bundle's sidecar names must unwrap under the SAME
	// team key the source run used — proving the copy is usable, not just
	// present.
	unwrapped, err := reckey.UnwrapDataKey(got.WrappedDataKey, teamKey)
	if err != nil {
		t.Fatalf("UnwrapDataKey via the copied bundle sidecar: %v", err)
	}
	for i, b := range unwrapped {
		if b != dataKey[i] {
			t.Fatalf("unwrapped data key does not match the original")
		}
	}

	var names []string
	for _, f := range res.Files {
		names = append(names, f)
	}
	found := false
	for _, n := range names {
		if n == runs.EncryptionFile {
			found = true
		}
	}
	if !found {
		t.Fatalf("AcceptResult.Files = %v, want it to list %s", names, runs.EncryptionFile)
	}
}

// TestAcceptWithNoEncryptionSidecarStillPromotes is the absence case: a run
// with no encrypt-mode field has no encryption.json, and that must not
// block or alter an ordinary promotion.
func TestAcceptWithNoEncryptionSidecarStillPromotes(t *testing.T) {
	cwd := t.TempDir()
	root, runID := acceptFixture(t, cwd)

	res, err := Accept(acceptOpts(cwd, root, runID))
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if _, err := os.Stat(filepath.Join(res.Dir, runs.EncryptionFile)); err == nil {
		t.Fatal("encryption.json exists in the bundle for a run that never had one")
	}
	got, err := runs.ReadEncryption(runs.Paths{RunDir: res.Dir})
	if err != nil {
		t.Fatalf("ReadEncryption(bundle): %v", err)
	}
	if got != nil {
		t.Fatalf("ReadEncryption(bundle) = %+v, want nil for a bundle with no sidecar", *got)
	}
}
