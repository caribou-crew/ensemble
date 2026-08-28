package reckey

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/caribou-crew/ensemble/retrace/runs"
)

// writeRunEncryption drops an encryption.json under a bare run directory
// (no manifest/wire.jsonl — Rekey only ever reads and writes the sidecar,
// so this is the minimal fixture it needs), wrapping dataKey under
// wrapKey.
func writeRunEncryption(t *testing.T, dir string, dataKey, wrapKey []byte) {
	t.Helper()
	wrapped, err := WrapDataKey(dataKey, wrapKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := runs.WriteEncryption(runs.Paths{RunDir: dir}, runs.Encryption{
		KeyID: KeyID(wrapKey), WrappedDataKey: wrapped,
	}); err != nil {
		t.Fatal(err)
	}
}

func readWrappedKeyID(t *testing.T, dir string) string {
	t.Helper()
	e, err := runs.ReadEncryption(runs.Paths{RunDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if e == nil {
		t.Fatalf("no encryption.json under %s", dir)
	}
	return e.KeyID
}

// TestRekeyRewrapsRunsAndRefBundles is task 8.4's central scenario: two
// encrypted runs and one reference bundle, all rewrapped in one pass, and
// every field still decrypts under --new while no longer decrypting under
// --old.
func TestRekeyRewrapsRunsAndRefBundles(t *testing.T) {
	cwd := t.TempDir()
	oldKey, newKey := bytes32('o'), bytes32('n')

	dataKeyA, err := GenerateDataKey()
	if err != nil {
		t.Fatal(err)
	}
	dataKeyB, err := GenerateDataKey()
	if err != nil {
		t.Fatal(err)
	}
	dataKeyRef, err := GenerateDataKey()
	if err != nil {
		t.Fatal(err)
	}

	runA := filepath.Join(runs.RunsRoot(cwd), "web", "checkout", "20260101T000000Z-aaaaaaa")
	runB := filepath.Join(runs.RunsRoot(cwd), "web", "login", "20260101T000000Z-bbbbbbb")
	refDir := filepath.Join(runs.RefsRoot(cwd), "web", "checkout", runs.RefRunID)
	for _, dir := range []string{runA, runB, refDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeRunEncryption(t, runA, dataKeyA, oldKey)
	writeRunEncryption(t, runB, dataKeyB, oldKey)
	writeRunEncryption(t, refDir, dataKeyRef, oldKey)

	res, err := Rekey(RekeyOptions{Cwd: cwd, Old: oldKey, New: newKey})
	if err != nil {
		t.Fatalf("Rekey: %v", err)
	}
	if len(res.Rewrapped()) != 3 {
		t.Fatalf("rewrapped %d entries, want 3: %+v", len(res.Rewrapped()), res.Entries)
	}
	if len(res.NeedsAttention()) != 0 {
		t.Fatalf("NeedsAttention = %+v, want none", res.NeedsAttention())
	}

	for _, dir := range []string{runA, runB, refDir} {
		if got := readWrappedKeyID(t, dir); got != KeyID(newKey) {
			t.Fatalf("%s: KeyID = %q, want the new key's %q", dir, got, KeyID(newKey))
		}
		e, err := runs.ReadEncryption(runs.Paths{RunDir: dir})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := UnwrapDataKey(e.WrappedDataKey, newKey); err != nil {
			t.Fatalf("%s: does not decrypt under --new: %v", dir, err)
		}
		if _, err := UnwrapDataKey(e.WrappedDataKey, oldKey); err == nil {
			t.Fatalf("%s: still decrypts under --old after rekey — the old key was not actually replaced", dir)
		}
	}
}

// TestRekeyRerunIsANoOp is the resumed/idempotent-rerun half of 8.4: running
// the exact same rotation twice must not error, and must not attempt (or
// report) a second rewrap.
func TestRekeyRerunIsANoOp(t *testing.T) {
	cwd := t.TempDir()
	oldKey, newKey := bytes32('o'), bytes32('n')
	dataKey, err := GenerateDataKey()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(runs.RunsRoot(cwd), "web", "checkout", "20260101T000000Z-aaaaaaa")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRunEncryption(t, dir, dataKey, oldKey)

	if _, err := Rekey(RekeyOptions{Cwd: cwd, Old: oldKey, New: newKey}); err != nil {
		t.Fatalf("first Rekey: %v", err)
	}

	res, err := Rekey(RekeyOptions{Cwd: cwd, Old: oldKey, New: newKey})
	if err != nil {
		t.Fatalf("second Rekey: %v", err)
	}
	if len(res.Rewrapped()) != 0 {
		t.Fatalf("second pass rewrapped %d entries, want 0 (already on --new)", len(res.Rewrapped()))
	}
	if len(res.NeedsAttention()) != 0 {
		t.Fatalf("NeedsAttention = %+v, want none — 'already on --new' is not something to flag", res.NeedsAttention())
	}
	found := false
	for _, e := range res.Skipped() {
		if e.Action == RekeyAlreadyCurrent {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected an already-current entry, got %+v", res.Entries)
	}
}

// TestRekeyLeavesAThirdUnrelatedKeyUntouchedAndReported is 8.4's third
// case: a run wrapped by neither --old nor --new must be reported, not
// silently left half-migrated (which "silently skip" would risk hiding).
func TestRekeyLeavesAThirdUnrelatedKeyUntouchedAndReported(t *testing.T) {
	cwd := t.TempDir()
	oldKey, newKey, thirdKey := bytes32('o'), bytes32('n'), bytes32('z')
	dataKey, err := GenerateDataKey()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(runs.RunsRoot(cwd), "web", "checkout", "20260101T000000Z-aaaaaaa")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRunEncryption(t, dir, dataKey, thirdKey)

	res, err := Rekey(RekeyOptions{Cwd: cwd, Old: oldKey, New: newKey})
	if err != nil {
		t.Fatalf("Rekey: %v", err)
	}
	if len(res.Rewrapped()) != 0 {
		t.Fatalf("rewrapped %d entries, want 0 — nothing here is wrapped under --old", len(res.Rewrapped()))
	}
	needs := res.NeedsAttention()
	if len(needs) != 1 {
		t.Fatalf("NeedsAttention = %+v, want exactly one entry for the unrelated-key file", needs)
	}
	if got := readWrappedKeyID(t, dir); got != KeyID(thirdKey) {
		t.Fatalf("the untouched file's KeyID changed to %q — it must stay wrapped under the third key", got)
	}
}
