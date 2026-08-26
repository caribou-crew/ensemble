package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readinessBase is a clean config with one routable service, matching the
// shape gatewayBase() uses for gateway tests.
func readinessBase(dir string) *Config {
	return &Config{
		Dir: dir,
		Services: map[string]Service{
			"catalog": {Run: "./catalog", Port: 8081, Proxy: 9081},
		},
	}
}

func writeReadinessFile(t *testing.T, dir, contents string) string {
	t.Helper()
	path := filepath.Join(dir, "readiness.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write readiness file: %v", err)
	}
	return path
}

func TestValidateReadinessClean(t *testing.T) {
	dir := t.TempDir()
	writeReadinessFile(t, dir, `
checks:
  - name: catalog-up
    service: catalog
    path: /healthz
    assert:
      status: 200
`)
	c := readinessBase(dir)
	c.Readiness = &Readiness{File: "readiness.yaml"}
	if err := c.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	checks := c.ReadinessChecks()
	if checks == nil || len(checks.Checks) != 1 || checks.Checks[0].Name != "catalog-up" {
		t.Fatalf("expected parsed checks cached on Config, got %+v", checks)
	}
}

func TestValidateReadinessNoKeyConfigured(t *testing.T) {
	c := readinessBase(t.TempDir())
	if err := c.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.ReadinessChecks() != nil {
		t.Fatalf("expected no cached checks, got %+v", c.ReadinessChecks())
	}
}

func TestValidateReadinessMissingFile(t *testing.T) {
	c := readinessBase(t.TempDir())
	c.Readiness = &Readiness{File: "does-not-exist.yaml"}
	err := c.Validate()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "does-not-exist.yaml") {
		t.Errorf("error does not name the missing file: %v", err)
	}
}

func TestValidateReadinessUnknownService(t *testing.T) {
	dir := t.TempDir()
	writeReadinessFile(t, dir, `
checks:
  - name: ghost-up
    service: ghost
    path: /healthz
    assert:
      status: 200
`)
	c := readinessBase(dir)
	c.Readiness = &Readiness{File: "readiness.yaml"}
	err := c.Validate()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "ghost-up") || !strings.Contains(err.Error(), "ghost") {
		t.Errorf("error does not name the check and unknown service: %v", err)
	}
}

func TestValidateReadinessDuplicateNames(t *testing.T) {
	dir := t.TempDir()
	writeReadinessFile(t, dir, `
checks:
  - name: catalog-up
    service: catalog
    path: /healthz
  - name: catalog-up
    service: catalog
    path: /readyz
`)
	c := readinessBase(dir)
	c.Readiness = &Readiness{File: "readiness.yaml"}
	err := c.Validate()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "duplicate check name") || !strings.Contains(err.Error(), "catalog-up") {
		t.Errorf("error does not identify the duplicate name: %v", err)
	}
}

func TestValidateReadinessNegativeTimeouts(t *testing.T) {
	dir := t.TempDir()
	writeReadinessFile(t, dir, `checks: []`)
	c := readinessBase(dir)
	c.Readiness = &Readiness{File: "readiness.yaml", TimeoutS: -1, RetryIntervalS: -1}
	err := c.Validate()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "timeout_s") || !strings.Contains(err.Error(), "retry_interval_s") {
		t.Errorf("error does not name both bad fields: %v", err)
	}
}

func TestReadinessEffectiveDefaults(t *testing.T) {
	var r Readiness
	if got := r.EffectiveTimeoutS(); got != DefaultReadinessTimeoutS {
		t.Errorf("EffectiveTimeoutS() = %d, want default %d", got, DefaultReadinessTimeoutS)
	}
	if got := r.EffectiveRetryIntervalS(); got != DefaultReadinessRetryIntervalS {
		t.Errorf("EffectiveRetryIntervalS() = %d, want default %d", got, DefaultReadinessRetryIntervalS)
	}

	r = Readiness{TimeoutS: 120, RetryIntervalS: 10}
	if got := r.EffectiveTimeoutS(); got != 120 {
		t.Errorf("EffectiveTimeoutS() = %d, want 120", got)
	}
	if got := r.EffectiveRetryIntervalS(); got != 10 {
		t.Errorf("EffectiveRetryIntervalS() = %d, want 10", got)
	}
}
