package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// latencyProfileBase is a clean config with one routable service, matching
// the shape readinessBase() uses for readiness tests.
func latencyProfileBase(dir string) *Config {
	return &Config{
		Dir: dir,
		Services: map[string]Service{
			"billing": {Run: "./billing", Port: 8081, Proxy: 9081},
		},
	}
}

func writeLatencyProfileFile(t *testing.T, dir, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "latency-production.yaml"), []byte(contents), 0o600); err != nil {
		t.Fatalf("write latency profile file: %v", err)
	}
}

func TestValidateLatencyProfileClean(t *testing.T) {
	dir := t.TempDir()
	writeLatencyProfileFile(t, dir, `
rules:
  - target: billing
    path: /
    from_datadog:
      query: "p{P}:trace.http.server.request.duration{service:billing,env:prod}"
      window_minutes: 60
  - target: "*"
    path: /health
    fixed_ms: 5
`)
	c := latencyProfileBase(dir)
	c.Latency.Profiles = map[string]LatencyProfile{"production": {File: "latency-production.yaml"}}
	if err := c.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	f := c.LatencyProfile("production")
	if f == nil || len(f.Rules) != 2 {
		t.Fatalf("expected parsed profile cached on Config, got %+v", f)
	}
	if names := c.LatencyProfileNames(); len(names) != 1 || names[0] != "production" {
		t.Fatalf("LatencyProfileNames() = %v", names)
	}
}

func TestValidateLatencyProfileNoKeyConfigured(t *testing.T) {
	c := latencyProfileBase(t.TempDir())
	if err := c.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.LatencyProfile("production") != nil {
		t.Fatalf("expected no cached profile")
	}
}

func TestValidateLatencyProfileMissingFile(t *testing.T) {
	c := latencyProfileBase(t.TempDir())
	c.Latency.Profiles = map[string]LatencyProfile{"production": {File: "does-not-exist.yaml"}}
	err := c.Validate()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "does-not-exist.yaml") {
		t.Errorf("error does not name the missing file: %v", err)
	}
}

func TestValidateLatencyProfileMalformedYAML(t *testing.T) {
	dir := t.TempDir()
	writeLatencyProfileFile(t, dir, "rules: [this is not valid: yaml: at all")
	c := latencyProfileBase(dir)
	c.Latency.Profiles = map[string]LatencyProfile{"production": {File: "latency-production.yaml"}}
	if err := c.Validate(); err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestValidateLatencyProfileRuleWithBothSources(t *testing.T) {
	dir := t.TempDir()
	writeLatencyProfileFile(t, dir, `
rules:
  - target: billing
    path: /
    fixed_ms: 25
    from_datadog:
      query: "p{P}:trace.http.server.request.duration{service:billing}"
`)
	c := latencyProfileBase(dir)
	c.Latency.Profiles = map[string]LatencyProfile{"production": {File: "latency-production.yaml"}}
	err := c.Validate()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "production") || !strings.Contains(err.Error(), "exactly one of from_datadog or fixed_ms") {
		t.Errorf("error does not identify the conflict: %v", err)
	}
}

func TestValidateLatencyProfileRuleWithNeitherSource(t *testing.T) {
	dir := t.TempDir()
	writeLatencyProfileFile(t, dir, `
rules:
  - target: billing
    path: /
`)
	c := latencyProfileBase(dir)
	c.Latency.Profiles = map[string]LatencyProfile{"production": {File: "latency-production.yaml"}}
	err := c.Validate()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "exactly one of from_datadog or fixed_ms") {
		t.Errorf("error does not identify the missing source: %v", err)
	}
}

func TestValidateLatencyProfileRuleUnknownTarget(t *testing.T) {
	dir := t.TempDir()
	writeLatencyProfileFile(t, dir, `
rules:
  - target: ghost
    path: /
    fixed_ms: 10
`)
	c := latencyProfileBase(dir)
	c.Latency.Profiles = map[string]LatencyProfile{"production": {File: "latency-production.yaml"}}
	err := c.Validate()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "production") || !strings.Contains(err.Error(), "ghost") {
		t.Errorf("error does not name the profile and unknown target: %v", err)
	}
}

func TestValidateLatencyProfileWildcardTargetAllowed(t *testing.T) {
	dir := t.TempDir()
	writeLatencyProfileFile(t, dir, `
rules:
  - target: "*"
    path: /
    fixed_ms: 10
`)
	c := latencyProfileBase(dir)
	c.Latency.Profiles = map[string]LatencyProfile{"production": {File: "latency-production.yaml"}}
	if err := c.Validate(); err != nil {
		t.Fatalf("unexpected error for wildcard target: %v", err)
	}
}

// The real process environment must win over .env — same precedence
// expandEnvVars already guarantees for "${VAR}" references in
// ensemble.yaml, now reused for values that only ever *name* an env var
// (e.g. datadog.api_key_env) rather than embedding one directly.
func TestLookupEnvRealEnvOverridesDotEnv(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, ".env", "DD_API_KEY=from-dotenv\n")
	writeFile(t, dir, "ensemble.yaml", `
services:
  bff:
    run: "node dist/main.js"
    port: 8003
`)
	t.Setenv("DD_API_KEY", "from-real-env")

	c, err := Load(filepath.Join(dir, "ensemble.yaml"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got, ok := c.LookupEnv("DD_API_KEY"); !ok || got != "from-real-env" {
		t.Errorf("LookupEnv(DD_API_KEY) = (%q, %v), want (from-real-env, true)", got, ok)
	}
}

func TestLookupEnvFallsBackToDotEnv(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, ".env", "DD_APP_KEY=from-dotenv\n")
	writeFile(t, dir, "ensemble.yaml", `
services:
  bff:
    run: "node dist/main.js"
    port: 8003
`)
	c, err := Load(filepath.Join(dir, "ensemble.yaml"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got, ok := c.LookupEnv("DD_APP_KEY"); !ok || got != "from-dotenv" {
		t.Errorf("LookupEnv(DD_APP_KEY) = (%q, %v), want (from-dotenv, true)", got, ok)
	}
}

func TestLookupEnvMissing(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "ensemble.yaml", `
services:
  bff:
    run: "node dist/main.js"
    port: 8003
`)
	c, err := Load(filepath.Join(dir, "ensemble.yaml"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := c.LookupEnv("DD_API_KEY"); ok {
		t.Errorf("expected DD_API_KEY to be absent")
	}
}

func TestDatadogConfigDefaults(t *testing.T) {
	c := &Config{}
	if got := c.DatadogSite(); got != DefaultDatadogSite {
		t.Errorf("DatadogSite() = %q, want default %q", got, DefaultDatadogSite)
	}
	if got := c.DatadogAPIKeyEnvName(); got != DefaultDatadogAPIKeyEnv {
		t.Errorf("DatadogAPIKeyEnvName() = %q, want default %q", got, DefaultDatadogAPIKeyEnv)
	}
	if got := c.DatadogAppKeyEnvName(); got != DefaultDatadogAppKeyEnv {
		t.Errorf("DatadogAppKeyEnvName() = %q, want default %q", got, DefaultDatadogAppKeyEnv)
	}
	if got := c.DatadogDefaultWindowMinutes(); got != DefaultDatadogWindowMinutes {
		t.Errorf("DatadogDefaultWindowMinutes() = %d, want default %d", got, DefaultDatadogWindowMinutes)
	}
	if got := c.DatadogServiceName("billing"); got != "billing" {
		t.Errorf("DatadogServiceName(billing) = %q, want unchanged", got)
	}
}

func TestDatadogConfigOverridesAndServiceMap(t *testing.T) {
	c := &Config{Datadog: &DatadogConfig{
		Site:                 "datadoghq.eu",
		APIKeyEnv:            "PROD_DD_KEY",
		AppKeyEnv:            "PROD_DD_APP",
		DefaultWindowMinutes: 30,
		ServiceMap:           map[string]string{"statements": "accounts-statements"},
	}}
	if got := c.DatadogSite(); got != "datadoghq.eu" {
		t.Errorf("DatadogSite() = %q", got)
	}
	if got := c.DatadogAPIKeyEnvName(); got != "PROD_DD_KEY" {
		t.Errorf("DatadogAPIKeyEnvName() = %q", got)
	}
	if got := c.DatadogAppKeyEnvName(); got != "PROD_DD_APP" {
		t.Errorf("DatadogAppKeyEnvName() = %q", got)
	}
	if got := c.DatadogDefaultWindowMinutes(); got != 30 {
		t.Errorf("DatadogDefaultWindowMinutes() = %d", got)
	}
	if got := c.DatadogServiceName("statements"); got != "accounts-statements" {
		t.Errorf("DatadogServiceName(statements) = %q", got)
	}
	if got := c.DatadogServiceName("billing"); got != "billing" {
		t.Errorf("DatadogServiceName(billing) = %q, want unmapped passthrough", got)
	}
}
