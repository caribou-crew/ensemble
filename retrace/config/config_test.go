package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caribou-crew/ensemble/retrace/rules"
)

func TestLoadParsesFlowsRulesAndDefaults(t *testing.T) {
	dir := t.TempDir()
	yaml := `
app: web
entry: edge-gw
flows:
  checkout:
    command: npx playwright test checkout.spec.ts
    perf_budget_ms: 4200
wire_ignore:
  - "**.requestId"
wire_rules:
  - headers: { x-request-id: uuid }
  - path: /cart
    body: { updatedAt: iso8601 }
path_normalize:
  - { pattern: "/users/[0-9]+", replacement: "/users/:id" }
expected_statuses:
  - { path: /api/flaky, status: 503 }
hop_require:
  - { method: POST, path: /payments/**, status: 201 }
masks:
  cart: [{ x: 10, y: 20, width: 100, height: 40 }]
`
	os.WriteFile(filepath.Join(dir, "retrace.yaml"), []byte(yaml), 0o644)
	cfg, err := Load(filepath.Join(dir, "retrace.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Thresholds.Gate != 0.1 || cfg.Thresholds.Fine != 0.05 {
		t.Fatalf("thresholds must default to 0.1/0.05, got %+v", cfg.Thresholds)
	}
	if got := cfg.NormalizePath("/users/42/cart"); got != "/users/:id/cart" {
		t.Fatalf("NormalizePath = %q", got)
	}
	if _, err := cfg.Rules(); err != nil {
		t.Fatalf("Rules: %v", err)
	}
	if cfg.Dir != dir {
		t.Fatalf("Dir = %q, want %q", cfg.Dir, dir)
	}
}

func TestAppendWireRuleWritesTheOverlayAndDiscoverMergesItAfterYaml(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "retrace.yaml"), []byte("app: web\n"), 0o644)
	if err := AppendWireRule(dir, rules.Raw{Path: "/cart", Body: map[string]any{"total": "integer"}}); err != nil {
		t.Fatalf("AppendWireRule: %v", err)
	}
	cfg, err := Discover(dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(cfg.WireRules) != 1 || cfg.WireRules[0].Path != "/cart" {
		t.Fatalf("overlay rule not merged: %+v", cfg.WireRules)
	}
	// Appending twice must not duplicate an identical rule — the review
	// queue's `rule` verb is idempotent by design.
	AppendWireRule(dir, rules.Raw{Path: "/cart", Body: map[string]any{"total": "integer"}})
	cfg, _ = Discover(dir)
	if len(cfg.WireRules) != 1 {
		t.Fatalf("duplicate rule appended: %+v", cfg.WireRules)
	}
}

// An unknown matcher must fail Load and NAME the offending rule. A config
// error that says only "invalid matcher" costs an editing round-trip on a
// file that may hold fifty rules.
func TestAnInvalidMatcherFailsLoadNamingTheRule(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "retrace.yaml")
	os.WriteFile(path, []byte("wire_rules:\n  - path: /cart\n    headers:\n      date: httpdate\n"), 0o644)

	_, err := Load(path)
	if err == nil {
		t.Fatal("an unknown matcher must fail Load")
	}
	if !strings.Contains(err.Error(), "wireRules[0].headers.date") {
		t.Fatalf("error must name the rule, got: %v", err)
	}
	if !strings.Contains(err.Error(), "http-date") {
		t.Fatalf("error should suggest the real matcher name, got: %v", err)
	}
}

func TestMasksForFallsBackToTheWildcardCheckpoint(t *testing.T) {
	c := &Config{Masks: map[string][]Rect{"*": {{X: 0, Y: 0, Width: 10, Height: 10}}}}
	if got := c.MasksFor("checkout", "cart"); len(got) != 1 || got[0].Width != 10 {
		t.Fatalf(`the "*" key must apply to every checkpoint, got %+v`, got)
	}
	c.Masks["cart"] = []Rect{{X: 1, Y: 1, Width: 20, Height: 20}}
	if got := c.MasksFor("checkout", "cart"); len(got) != 1 || got[0].Width != 20 {
		t.Fatalf("a named checkpoint must win over the wildcard, got %+v", got)
	}
}

// TestAYamlKeyTypoIsAnErrorNamingTheKey pins the KnownFields behaviour so a
// missing/wrong yaml tag (or a plain typo in the file) surfaces as a hard
// Load error naming the bad key, rather than silently being ignored. This
// is what makes the explicit yaml: tags on every field load-bearing rather
// than decoration.
func TestAYamlKeyTypoIsAnErrorNamingTheKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "retrace.yaml")
	os.WriteFile(path, []byte("app: web\nwire_ignoreX:\n  - foo\n"), 0o644)

	_, err := Load(path)
	if err == nil {
		t.Fatal("a typo'd yaml key must fail Load")
	}
	if !strings.Contains(err.Error(), "wire_ignoreX") {
		t.Fatalf("error must name the offending key, got: %v", err)
	}
}

// TestZeroThresholdsAreTreatedAsUnsetNotAsZeroGate pins the zero-value
// constraint for Thresholds: an absent (zero) Gate/Fine must mean "use the
// default", not "gate/tolerate at literally 0". This is the "absent and
// permissive are different meanings" trap: a caller who forgets to set
// thresholds must get the documented default behavior, not a threshold of
// 0 baked in silently. Written by mutating applyDefaults to skip Thresholds
// and watching this fail (see task-3-report.md for the RED transcript).
func TestZeroThresholdsAreTreatedAsUnsetNotAsZeroGate(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "retrace.yaml"), []byte("app: web\n"), 0o644)
	cfg, err := Load(filepath.Join(dir, "retrace.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Thresholds.Gate != DefaultGate {
		t.Fatalf("Thresholds.Gate left unset must default to DefaultGate, got %v", cfg.Thresholds.Gate)
	}
	if cfg.Thresholds.Fine != DefaultFine {
		t.Fatalf("Thresholds.Fine left unset must default to DefaultFine, got %v", cfg.Thresholds.Fine)
	}
}
