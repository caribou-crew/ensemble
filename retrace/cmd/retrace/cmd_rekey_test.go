package main

// cmd_rekey_test.go drives `retrace rekey` through a BUILT binary, never
// `go run` (global-constraints.md) — the wiring this proves is main.go's
// dispatch and flag parsing, not the rotation logic itself (covered at the
// unit level in retrace/reckey).

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caribou-crew/ensemble/retrace/reckey"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

func TestRekeyCLIRotatesAnEncryptedRunAndExitsZero(t *testing.T) {
	bin := buildRetrace(t)
	cwd := t.TempDir()

	oldKey, err := reckey.GenerateDataKey()
	if err != nil {
		t.Fatal(err)
	}
	newKey, err := reckey.GenerateDataKey()
	if err != nil {
		t.Fatal(err)
	}
	dataKey, err := reckey.GenerateDataKey()
	if err != nil {
		t.Fatal(err)
	}
	runDir := filepath.Join(runs.RunsRoot(cwd), "web", "checkout", "20260101T000000Z-aaaaaaa")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	wrapped, err := reckey.WrapDataKey(dataKey, oldKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := runs.WriteEncryption(runs.Paths{RunDir: runDir}, runs.Encryption{
		KeyID: reckey.KeyID(oldKey), WrappedDataKey: wrapped,
	}); err != nil {
		t.Fatal(err)
	}

	res := runRetrace(t, bin, cwd, "", "rekey",
		"--old", hex.EncodeToString(oldKey), "--new", hex.EncodeToString(newKey))
	if res.code != 0 {
		t.Fatalf("rekey exit = %d, want 0\nstdout: %s\nstderr: %s", res.code, res.stdout, res.stderr)
	}
	if !strings.Contains(res.stdout, "1 rewrapped") {
		t.Fatalf("stdout = %q, want it to report 1 rewrapped", res.stdout)
	}

	e, err := runs.ReadEncryption(runs.Paths{RunDir: runDir})
	if err != nil {
		t.Fatal(err)
	}
	if e.KeyID != reckey.KeyID(newKey) {
		t.Fatalf("KeyID = %q, want the new key's %q", e.KeyID, reckey.KeyID(newKey))
	}
}

func TestRekeyCLIInitWritesAKeyfileThenRefusesASecondTime(t *testing.T) {
	bin := buildRetrace(t)
	cwd := t.TempDir()

	res := runRetrace(t, bin, cwd, "", "rekey", "--init")
	if res.code != 0 {
		t.Fatalf("rekey --init exit = %d, want 0\nstdout: %s\nstderr: %s", res.code, res.stdout, res.stderr)
	}
	path := filepath.Join(cwd, ".retrace", "recording.key")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the written keyfile: %v", err)
	}
	if len(b) != 32 {
		t.Fatalf("keyfile is %d bytes, want 32", len(b))
	}

	again := runRetrace(t, bin, cwd, "", "rekey", "--init")
	if again.code == 0 {
		t.Fatalf("a second --init succeeded — it must refuse to overwrite an existing keyfile\nstdout: %s", again.stdout)
	}
}

func TestRekeyCLIRequiresBothOldAndNew(t *testing.T) {
	bin := buildRetrace(t)
	cwd := t.TempDir()

	res := runRetrace(t, bin, cwd, "", "rekey", "--old", "deadbeef")
	if res.code == 0 {
		t.Fatalf("rekey with only --old succeeded, want a usage error\nstdout: %s", res.stdout)
	}
}
