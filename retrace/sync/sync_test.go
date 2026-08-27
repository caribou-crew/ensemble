package sync

import (
	"testing"
	"time"
)

func TestParseSinceDays(t *testing.T) {
	got, err := ParseSince("7d")
	if err != nil {
		t.Fatalf("ParseSince(7d): %v", err)
	}
	if want := 7 * 24 * time.Hour; got != want {
		t.Errorf("ParseSince(7d) = %v, want %v", got, want)
	}
}

func TestParseSinceHoursMinutesSeconds(t *testing.T) {
	cases := map[string]time.Duration{
		"24h": 24 * time.Hour,
		"30m": 30 * time.Minute,
		"45s": 45 * time.Second,
	}
	for in, want := range cases {
		got, err := ParseSince(in)
		if err != nil {
			t.Fatalf("ParseSince(%q): %v", in, err)
		}
		if got != want {
			t.Errorf("ParseSince(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestParseSinceRejectsGarbage(t *testing.T) {
	for _, in := range []string{"", "a week", "-7d", "0d", "7", "7x"} {
		if _, err := ParseSince(in); err == nil {
			t.Errorf("ParseSince(%q) accepted, want an error", in)
		}
	}
}

func TestRunRejectsEmptyCwd(t *testing.T) {
	if _, err := Run(Options{From: "github", Repo: "org/repo"}); err == nil {
		t.Fatal("expected an error for an empty Cwd")
	}
}
