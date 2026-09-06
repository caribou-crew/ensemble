package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/caribou-crew/ensemble/core/trace"
)

func TestOpenHopHistoryCountsRecoveredNewlineBeforeRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hops.jsonl")
	original := []byte(`{"seq":7}`)
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	next := trace.Hop{Seq: 8, Path: "/new"}
	var encoded bytes.Buffer
	if err := trace.NewWriter(&encoded).Write(next); err != nil {
		t.Fatal(err)
	}
	// Without counting the recovered delimiter, the new record would fit
	// exactly; with it counted, the old log must rotate before the write.
	f, seq, err := openHopHistory(path, int64(len(original)+encoded.Len()), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if seq != 7 {
		t.Fatalf("initial seq=%d, want 7", seq)
	}
	if err := trace.NewWriter(f).Write(next); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	prior, err := os.ReadFile(path + ".1")
	if err != nil {
		t.Fatalf("recovered newline was not counted by rotation: %v", err)
	}
	if !bytes.Equal(prior, append(original, '\n')) {
		t.Errorf("rotated original=%q, want original bytes plus newline", prior)
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(current, encoded.Bytes()) {
		t.Errorf("new generation=%q, want exactly the new record", current)
	}
}

func TestOpenHopHistoryLeavesTerminatedOrEmptyLogUnchanged(t *testing.T) {
	for _, original := range []string{"", "{\"seq\":7}\n"} {
		t.Run(original, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "hops.jsonl")
			if err := os.WriteFile(path, []byte(original), 0600); err != nil {
				t.Fatal(err)
			}
			f, _, err := openHopHistory(path, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != original {
				t.Errorf("startup changed an already delimited or empty log: %q", got)
			}
		})
	}
}
