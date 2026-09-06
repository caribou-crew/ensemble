package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRetainedHopSequenceScansEveryRetainedGeneration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hops.jsonl")
	writeSequenceHistory(t, path, 10, 8)
	writeSequenceHistory(t, path+".1", 20, 15)
	// A missing generation is fine; the maximum may still be in .3.
	writeSequenceHistory(t, path+".3", 40, 30)
	writeSequenceHistory(t, path+".4", 9000)
	f, err := os.OpenFile(path+".3", os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString("{truncated")
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	got, err := retainedHopSequence(path, 3)
	if err != nil || got != 40 {
		t.Fatalf("retained maximum = %d, %v; want 40", got, err)
	}
}

func TestRetainedHopSequenceFailsWhenHistoryCannotBeScanned(t *testing.T) {
	for _, failure := range []string{"unreadable", "oversized line", "exhausted"} {
		t.Run(failure, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "hops.jsonl")
			switch failure {
			case "unreadable":
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			case "oversized line":
				if err := os.WriteFile(path, []byte(strings.Repeat("x", 16*1024*1024+1)), 0o600); err != nil {
					t.Fatal(err)
				}
			case "exhausted":
				writeSequenceHistory(t, path, ^uint64(0))
			}
			if _, err := retainedHopSequence(path, 3); err == nil || !strings.Contains(err.Error(), path) {
				t.Fatalf("%s history must fail naming its path, got %v", failure, err)
			}
		})
	}
}
