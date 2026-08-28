package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAppendRedactEntryWritesTheOverlayAndDiscoverMergesItAfterYaml(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "retrace.yaml"), []byte("app: web\nredact:\n  - password\n"), 0o644)
	if err := AppendRedactEntry(dir, RedactEntry{Field: "account_number", Mode: "encrypt", Why: "checkout total needs the real value"}); err != nil {
		t.Fatalf("AppendRedactEntry: %v", err)
	}
	cfg, err := Discover(dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(cfg.Redact) != 2 {
		t.Fatalf("expected the yaml entry plus the overlay entry, got %+v", cfg.Redact)
	}
	if cfg.Redact[0].Field != "password" || cfg.Redact[0].Mode != "destroy" {
		t.Fatalf("yaml entry changed: %+v", cfg.Redact[0])
	}
	if cfg.Redact[1].Field != "account_number" || cfg.Redact[1].Mode != "encrypt" {
		t.Fatalf("overlay entry not merged: %+v", cfg.Redact[1])
	}

	// Idempotent, same as AppendWireRule.
	if err := AppendRedactEntry(dir, RedactEntry{Field: "account_number", Mode: "encrypt", Why: "checkout total needs the real value"}); err != nil {
		t.Fatalf("AppendRedactEntry (second): %v", err)
	}
	cfg, err = Discover(dir)
	if err != nil {
		t.Fatalf("Discover (second): %v", err)
	}
	if len(cfg.Redact) != 2 {
		t.Fatalf("duplicate entry appended: %+v", cfg.Redact)
	}
}

func TestAppendRedactEntryDefaultsModeToDestroy(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "retrace.yaml"), []byte("app: web\n"), 0o644)
	if err := AppendRedactEntry(dir, RedactEntry{Field: "session_id"}); err != nil {
		t.Fatalf("AppendRedactEntry: %v", err)
	}
	cfg, err := Discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Redact) != 1 || cfg.Redact[0].Mode != "destroy" {
		t.Fatalf("expected mode to default to destroy, got %+v", cfg.Redact)
	}
}

func TestAppendRedactEntryRejectsAnEmptyField(t *testing.T) {
	dir := t.TempDir()
	if err := AppendRedactEntry(dir, RedactEntry{Field: "  "}); err == nil {
		t.Fatal("AppendRedactEntry accepted an empty field name")
	}
}

func TestAppendRedactEntryRejectsAnUnknownMode(t *testing.T) {
	dir := t.TempDir()
	if err := AppendRedactEntry(dir, RedactEntry{Field: "card", Mode: "hide"}); err == nil {
		t.Fatal("AppendRedactEntry accepted an unknown mode")
	}
}
