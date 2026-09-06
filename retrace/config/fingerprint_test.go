package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestComparisonFingerprintUsesEffectiveConfigButExcludesItsDirectory(t *testing.T) {
	load := func(dir, body string) *Config {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "retrace.yaml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		cfg, err := Discover(dir)
		if err != nil {
			t.Fatalf("Discover: %v", err)
		}
		return cfg
	}
	fingerprint := func(cfg *Config) string {
		t.Helper()
		got, err := cfg.ComparisonFingerprint()
		if err != nil {
			t.Fatalf("ComparisonFingerprint: %v", err)
		}
		if len(got) != 64 {
			t.Fatalf("fingerprint %q is not a full SHA-256 hex digest", got)
		}
		return got
	}

	body := "app: web\nthresholds:\n  gate: 0.2\n  fine: 0.05\n"
	first := fingerprint(load(t.TempDir(), body))
	if moved := fingerprint(load(t.TempDir(), body)); moved != first {
		t.Errorf("same effective config at another directory = %s, want %s", moved, first)
	}
	if changed := fingerprint(load(t.TempDir(), "app: web\nthresholds:\n  gate: 0.3\n  fine: 0.05\n")); changed == first {
		t.Errorf("threshold policy changed but fingerprint stayed %s", first)
	}
}
