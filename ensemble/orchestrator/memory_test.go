package orchestrator

import (
	"context"
	"os"
	"testing"

	"github.com/caribou-crew/ensemble/ensemble/config"
)

func TestParseMemSizeKB(t *testing.T) {
	cases := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{"800B", 0, false},        // 800/1024 truncates to 0
		{"2048B", 2, false},       // 2048/1024 = 2
		{"512KiB", 512, false},
		{"12.5MiB", 12800, false}, // 12.5 * 1024
		{"1.2GiB", 1258291, false}, // 1.2 * 1024 * 1024
		{"1K", 1, false},
		{"3M", 3072, false},
		{"1G", 1048576, false},
		{"", 0, true},
		{"nope", 0, true},
		{"5XiB", 0, true},
	}
	for _, c := range cases {
		got, err := parseMemSizeKB(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseMemSizeKB(%q) = %d, want error", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseMemSizeKB(%q) unexpected error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("parseMemSizeKB(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

// sampleNativeRSSKB against the test binary's own PID proves the ps
// shell-out and KB parse actually work end to end, without spawning a
// throwaway process just to sample it.
func TestSampleNativeRSSKB(t *testing.T) {
	kb, err := sampleNativeRSSKB(context.Background(), os.Getpid())
	if err != nil {
		t.Fatalf("sampleNativeRSSKB: %v", err)
	}
	if kb <= 0 {
		t.Fatalf("kb = %d, want > 0 for the running test process", kb)
	}
}

func TestSampleNativeRSSKBDeadProcess(t *testing.T) {
	// PID 0 is never a valid target for `ps -p` on macOS/Linux.
	if _, err := sampleNativeRSSKB(context.Background(), 0); err == nil {
		t.Fatal("expected an error sampling pid 0")
	}
}

// WithMemory must populate RSSKB for a live native node and leave a
// stopped node untouched, without mutating the caller's slice.
func TestWithMemory(t *testing.T) {
	cfg := &config.Config{
		Dir: t.TempDir(),
		Services: map[string]config.Service{
			"svc": {Run: "sleep 30"},
		},
	}
	o := newTestOrchestrator(t, cfg, Opts{})
	if err := o.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	t.Cleanup(func() { o.Down() })

	states := o.States()
	orig := states[0].RSSKB

	enriched := o.WithMemory(context.Background(), states)
	if len(enriched) != len(states) {
		t.Fatalf("len(enriched) = %d, want %d", len(enriched), len(states))
	}
	var found bool
	for _, s := range enriched {
		if s.Name == "svc" {
			found = true
			if s.RSSKB <= 0 {
				t.Fatalf("RSSKB = %d, want > 0 for a live native process", s.RSSKB)
			}
		}
	}
	if !found {
		t.Fatal("svc missing from WithMemory output")
	}
	if states[0].RSSKB != orig {
		t.Fatalf("WithMemory mutated the caller's slice in place (RSSKB = %d, want %d)", states[0].RSSKB, orig)
	}
}
