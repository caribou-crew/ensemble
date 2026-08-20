package trace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeLines(t *testing.T, w *RotatingFile, lines ...string) {
	t.Helper()
	for _, l := range lines {
		if _, err := w.Write([]byte(l + "\n")); err != nil {
			t.Fatalf("write %q: %v", l, err)
		}
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func TestRotatingFileRollsOverAndKeepsGenerations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hops.jsonl")
	// 8 bytes: every 6-byte line ("aaaaa\n") rotates on the next write.
	w, err := OpenRotatingFile(path, 8, 2)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer w.Close()

	writeLines(t, w, "aaaaa", "bbbbb", "ccccc", "ddddd")

	if got := readFile(t, path); got != "ddddd\n" {
		t.Errorf("current file = %q, want the newest line", got)
	}
	if got := readFile(t, path+".1"); got != "ccccc\n" {
		t.Errorf("generation 1 = %q", got)
	}
	if got := readFile(t, path+".2"); got != "bbbbb\n" {
		t.Errorf("generation 2 = %q", got)
	}
	// keep=2, so the oldest generation is dropped rather than retained.
	if _, err := os.Stat(path + ".3"); !os.IsNotExist(err) {
		t.Errorf("generation 3 exists; keep=2 should have dropped it (err=%v)", err)
	}
}

func TestRotatingFileKeepsOwnerOnlyPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hops.jsonl")
	w, err := OpenRotatingFile(path, 8, 1)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer w.Close()

	writeLines(t, w, "aaaaa", "bbbbb")

	for _, p := range []string{path, path + ".1"} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatalf("stat %s: %v", p, err)
		}
		if perm := fi.Mode().Perm(); perm&0o077 != 0 {
			t.Errorf("%s mode = %#o, want no group/other bits", p, perm)
		}
	}
}

func TestRotatingFileNeverSplitsARecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hops.jsonl")
	w, err := OpenRotatingFile(path, 16, 1)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer w.Close()

	// One hop bigger than the whole budget: it must land intact (half an
	// NDJSON line is unparseable), not be truncated at the limit.
	big := strings.Repeat("x", 100) + "\n"
	writeLines(t, w, "seed")
	if _, err := w.Write([]byte(big)); err != nil {
		t.Fatalf("write big: %v", err)
	}
	if got := readFile(t, path); got != big {
		t.Errorf("oversized record was not written whole (%d bytes)", len(got))
	}
}

func TestRotatingFileDisabledWhenMaxBytesUnset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hops.jsonl")
	w, err := OpenRotatingFile(path, 0, 3)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer w.Close()

	writeLines(t, w, "aaaaa", "bbbbb", "ccccc")

	if got := readFile(t, path); got != "aaaaa\nbbbbb\nccccc\n" {
		t.Errorf("maxBytes=0 should append without rotating, got %q", got)
	}
	if _, err := os.Stat(path + ".1"); !os.IsNotExist(err) {
		t.Errorf("rotated despite maxBytes=0 (err=%v)", err)
	}
}

func TestRotatingFileResumesSizeBudgetAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hops.jsonl")

	w, err := OpenRotatingFile(path, 8, 1)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	writeLines(t, w, "aaaaa")
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Reopening must count what's already on disk — otherwise a process
	// that restarts often never reaches the limit and grows forever.
	w2, err := OpenRotatingFile(path, 8, 1)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer w2.Close()
	writeLines(t, w2, "bbbbb")

	if got := readFile(t, path); got != "bbbbb\n" {
		t.Errorf("current file = %q, want rotation to have happened on reopen", got)
	}
	if got := readFile(t, path+".1"); got != "aaaaa\n" {
		t.Errorf("generation 1 = %q", got)
	}
}

func TestRotatingFileWriteAfterCloseIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hops.jsonl")
	w, err := OpenRotatingFile(path, 0, 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("second close should be a no-op, got %v", err)
	}
	if _, err := w.Write([]byte("x\n")); err == nil {
		t.Error("write after close returned nil error")
	}
}
