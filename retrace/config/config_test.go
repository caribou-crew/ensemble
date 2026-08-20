package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
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

// TestConcurrentAppendWireRuleProducesNRulesWithNoLoss pins the CRITICAL
// finding: AppendWireRule's read-modify-write must be serialized. Before the
// fix, 8 concurrent appends on a fresh directory left 2 rules on disk (6
// silently lost, every call still returning nil). See task-3-report.md for
// the mutate/revert transcript.
func TestConcurrentAppendWireRuleProducesNRulesWithNoLoss(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "retrace.yaml"), []byte("app: web\n"), 0o644)

	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = AppendWireRule(dir, rules.Raw{Path: fmt.Sprintf("/item/%d", i)})
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("AppendWireRule(%d): %v", i, err)
		}
	}

	cfg, err := Discover(dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(cfg.WireRules) != n {
		t.Fatalf("want %d rules on disk, got %d: %+v", n, len(cfg.WireRules), cfg.WireRules)
	}
}

// TestConcurrentAppendNeverExposesATornOverlayToReaders pins the other half
// of the CRITICAL finding: a writer mid-append must never let a concurrent
// Discover observe a partial file. Before the fix, one appender racing four
// readers over a 50-rule overlay produced 92 "unexpected end of JSON input"
// failures.
func TestConcurrentAppendNeverExposesATornOverlayToReaders(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "retrace.yaml"), []byte("app: web\n"), 0o644)
	for i := 0; i < 50; i++ {
		if err := AppendWireRule(dir, rules.Raw{Path: fmt.Sprintf("/seed/%d", i)}); err != nil {
			t.Fatalf("seed append %d: %v", i, err)
		}
	}

	var writerWG sync.WaitGroup
	writerWG.Add(1)
	go func() {
		defer writerWG.Done()
		for i := 0; i < 50; i++ {
			AppendWireRule(dir, rules.Raw{Path: fmt.Sprintf("/writer/%d", i)})
		}
	}()

	stop := make(chan struct{})
	var readerWG sync.WaitGroup
	var readErrs int32
	for r := 0; r < 4; r++ {
		readerWG.Add(1)
		go func() {
			defer readerWG.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if _, err := Discover(dir); err != nil {
					atomic.AddInt32(&readErrs, 1)
				}
			}
		}()
	}

	writerWG.Wait()
	close(stop)
	readerWG.Wait()

	if readErrs != 0 {
		t.Fatalf("%d concurrent Discover calls observed a torn overlay", readErrs)
	}
}

// TestAppendWireRuleRejectsAnInvalidRule pins MAJOR finding 2: a bad matcher
// name must fail the append itself, not get written and brick every later
// Discover call in the project.
func TestAppendWireRuleRejectsAnInvalidRule(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "retrace.yaml"), []byte("app: web\n"), 0o644)

	err := AppendWireRule(dir, rules.Raw{Path: "/cart", Headers: map[string]any{"date": "httpdate"}})
	if err == nil {
		t.Fatal("AppendWireRule must reject an unknown matcher")
	}
	if !strings.Contains(err.Error(), "http-date") {
		t.Fatalf("error should suggest the real matcher name, got: %v", err)
	}

	// The project must still be usable afterwards — nothing was written.
	if _, err := Discover(dir); err != nil {
		t.Fatalf("a rejected append must not brick Discover: %v", err)
	}
}

// TestDiscoverAppliesDefaultsWhenNoConfigIsPresent pins MAJOR finding 3's
// no-config path: dropping applyDefaults from Discover's no-config branch
// (M1 in the review) left Thresholds at the zero value, which this project's
// zero-value rule says must never mean "fine".
func TestDiscoverAppliesDefaultsWhenNoConfigIsPresent(t *testing.T) {
	dir := t.TempDir()
	cfg, err := Discover(dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if cfg.Thresholds.Gate != DefaultGate || cfg.Thresholds.Fine != DefaultFine {
		t.Fatalf("Discover with no retrace.yaml must default Thresholds, got %+v", cfg.Thresholds)
	}
	if cfg.Loaded {
		t.Fatal("Loaded must be false when no retrace.yaml was found")
	}
}

// TestDiscoverLoadsRetraceYamlWhenPresent pins MAJOR finding 3's yaml
// branch: making Discover's os.Stat/Load branch unreachable (M20 in the
// review) was invisible to the old suite because nothing asserted on a
// yaml-derived field through Discover specifically.
func TestDiscoverLoadsRetraceYamlWhenPresent(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "retrace.yaml"), []byte("app: web\n"), 0o644)
	cfg, err := Discover(dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if cfg.App != "web" {
		t.Fatalf("Discover must load retrace.yaml, got App=%q", cfg.App)
	}
	if !cfg.Loaded {
		t.Fatal("Loaded must be true when Discover reads a real retrace.yaml")
	}
}

// TestAppendWireRuleNeverOverwritesExistingRules pins MAJOR finding 4's M10:
// replacing the append's `existing = append(existing, r)` with
// `existing = []rules.Raw{r}` clobbers every previously reviewed rule. The
// old suite only ever appended one rule, so an append-that-overwrites looked
// identical to a real append.
func TestAppendWireRuleNeverOverwritesExistingRules(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "retrace.yaml"), []byte("app: web\n"), 0o644)

	for _, p := range []string{"/a", "/b", "/c"} {
		if err := AppendWireRule(dir, rules.Raw{Path: p}); err != nil {
			t.Fatalf("AppendWireRule(%s): %v", p, err)
		}
	}
	cfg, err := Discover(dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(cfg.WireRules) != 3 {
		t.Fatalf("three appends must leave three rules, got %d: %+v", len(cfg.WireRules), cfg.WireRules)
	}
}

// TestOverlayRuleIsMergedAfterYamlAndCanOverrideIt pins MAJOR finding 4's
// M6: the doc comment's headline promise is that the overlay is merged
// AFTER the yaml rules, so a later reviewed rule can override a
// hand-written one. Merging it before survives the old suite silently.
func TestOverlayRuleIsMergedAfterYamlAndCanOverrideIt(t *testing.T) {
	dir := t.TempDir()
	// createdAt is set ONLY by the yaml rule; the overlay never touches it.
	// It must still resolve after the overlay merges in — a mutant that
	// replaces the merge with c.WireRules = overlay (discarding every
	// hand-written rule the moment one overlay rule exists) would resolve
	// this to "" while leaving the "total" assertion below green.
	yamlSrc := "app: web\nwire_rules:\n  - path: /cart\n    body: { total: integer, createdAt: iso8601 }\n"
	os.WriteFile(filepath.Join(dir, "retrace.yaml"), []byte(yamlSrc), 0o644)
	if err := AppendWireRule(dir, rules.Raw{Path: "/cart", Body: map[string]any{"total": "uuid"}}); err != nil {
		t.Fatalf("AppendWireRule: %v", err)
	}
	// A second overlay append, distinct from the first, to pin that the
	// on-disk overlay order is append order — an unpinned mutant that
	// prepends instead of appends (existing = append([]rules.Raw{r},
	// existing...)) would still leave the right COUNT of rules (M10 covers
	// count) but in the wrong order.
	if err := AppendWireRule(dir, rules.Raw{Path: "/other", Body: map[string]any{"x": "uuid"}}); err != nil {
		t.Fatalf("AppendWireRule: %v", err)
	}

	cfg, err := Discover(dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	rs, err := cfg.Rules()
	if err != nil {
		t.Fatalf("Rules: %v", err)
	}
	resolved := rules.Resolve(rs, "GET", "/cart")
	if got := resolved.ForField("total").Name; got != "uuid" {
		t.Fatalf(`overlay rule must win over the yaml rule for the same field, got matcher %q, want "uuid"`, got)
	}
	if got := resolved.ForField("createdAt").Name; got != "iso8601" {
		t.Fatalf(`a yaml-only field must still resolve after Discover merges the overlay, got matcher %q, want "iso8601"`, got)
	}

	onDisk, err := readOverlay(filepath.Join(dir, OverlayPath))
	if err != nil {
		t.Fatalf("readOverlay: %v", err)
	}
	if len(onDisk) != 2 || onDisk[0].Path != "/cart" || onDisk[1].Path != "/other" {
		t.Fatalf("on-disk overlay order must be append order, got %+v", onDisk)
	}
}

// TestMasksForPrefersFlowLevelMasksOverTopLevel pins MAJOR finding 4's M13:
// making MasksFor ignore flow-level masks entirely survived because the
// brief's own test only ever exercised the top-level map.
func TestMasksForPrefersFlowLevelMasksOverTopLevel(t *testing.T) {
	c := &Config{
		Masks: map[string][]Rect{"cart": {{X: 1, Y: 1, Width: 20, Height: 20}}},
		Flows: map[string]Flow{
			"checkout": {Masks: map[string][]Rect{"cart": {{X: 9, Y: 9, Width: 5, Height: 5}}}},
		},
	}
	got := c.MasksFor("checkout", "cart")
	if len(got) != 1 || got[0].Width != 5 {
		t.Fatalf("a flow-level mask must win over the top-level map, got %+v", got)
	}
}

// TestOverlayJSONRejectsUnknownFields pins MINOR finding 5: a mis-shaped
// rule in the machine-owned overlay must fail loudly rather than decode as
// an empty, match-everything rules.Raw — matching the strictness of the
// yaml side's KnownFields(true).
func TestOverlayJSONRejectsUnknownFields(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "retrace.yaml"), []byte("app: web\n"), 0o644)
	overlayDir := filepath.Join(dir, ".retrace")
	os.MkdirAll(overlayDir, 0o755)
	os.WriteFile(filepath.Join(overlayDir, "wire-rules.json"), []byte(`[{"pathh":"/cart","bodyy":{}}]`), 0o644)

	_, err := Discover(dir)
	if err == nil {
		t.Fatal("an unknown field in the overlay must be an error, not a silent match-everything rule")
	}
}

// TestLoadRejectsASecondYamlDocument pins MINOR finding 6: a `---` second
// document must not be silently dropped.
func TestLoadRejectsASecondYamlDocument(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "retrace.yaml")
	os.WriteFile(path, []byte("app: web\n---\nentry: gw\n"), 0o644)

	_, err := Load(path)
	if err == nil {
		t.Fatal("a second YAML document must fail Load, not be silently dropped")
	}
}

// TestGatesPixelDefaultsFromThresholdsGateAndZeroIsMeaningful pins the
// controller's ruling for item 1: gates.pixel absent must default from
// thresholds.gate (never zero, never ungated), but an EXPLICIT
// `budget_pct: 0` must round-trip to exactly 0, not be treated as unset.
// See task-C-config-shapes-report.md for the mutate/revert transcript that
// shows this test catching applyDefaults collapsing the two.
func TestGatesPixelDefaultsFromThresholdsGateAndZeroIsMeaningful(t *testing.T) {
	dir := t.TempDir()

	// No gates: block at all — pixel must still default to DefaultGate.
	os.WriteFile(filepath.Join(dir, "retrace.yaml"), []byte("app: web\n"), 0o644)
	cfg, err := Load(filepath.Join(dir, "retrace.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	g, ok := cfg.Gates["pixel"]
	if !ok || g.BudgetPct == nil {
		t.Fatalf("gates.pixel must be present and set when absent from config, got %+v", cfg.Gates)
	}
	if *g.BudgetPct != DefaultGate {
		t.Fatalf("gates.pixel.budget_pct with no gates: block = %v, want DefaultGate (%v)", *g.BudgetPct, DefaultGate)
	}

	// Explicit budget_pct: 0 must survive as exactly 0.
	path2 := filepath.Join(dir, "retrace2.yaml")
	os.WriteFile(path2, []byte("app: web\ngates:\n  pixel:\n    budget_pct: 0\n"), 0o644)
	cfg2, err := Load(path2)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	g2, ok := cfg2.Gates["pixel"]
	if !ok || g2.BudgetPct == nil {
		t.Fatalf("gates.pixel must be present when explicitly set to 0, got %+v", cfg2.Gates)
	}
	if *g2.BudgetPct != 0 {
		t.Fatalf("gates.pixel.budget_pct explicitly set to 0 must stay 0, got %v", *g2.BudgetPct)
	}

	// wire/hop/perf have no default and must stay absent (ungated).
	if _, ok := cfg.Gates["wire"]; ok {
		t.Fatalf("gates.wire must stay absent (ungated) when not configured, got %+v", cfg.Gates)
	}
}

// TestFailOnAndOtherPlaneGatesRoundTripThroughRealYaml is the item-1 text
// test for fail_on and the non-pixel planes: parses real YAML, not a struct
// literal, so the yaml tags are actually exercised.
func TestFailOnAndOtherPlaneGatesRoundTripThroughRealYaml(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "retrace.yaml")
	yamlSrc := "app: web\n" +
		"gates:\n" +
		"  pixel: { budget_pct: 0.2 }\n" +
		"  wire: { budget_pct: 0.05 }\n" +
		"  hop: { budget_pct: 0 }\n" +
		"fail_on: [\"pixel\", \"wire\"]\n"
	os.WriteFile(path, []byte(yamlSrc), 0o644)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Gates["pixel"].BudgetPct; got == nil || *got != 0.2 {
		t.Fatalf("gates.pixel.budget_pct = %v, want 0.2", got)
	}
	if got := cfg.Gates["wire"].BudgetPct; got == nil || *got != 0.05 {
		t.Fatalf("gates.wire.budget_pct = %v, want 0.05", got)
	}
	if got := cfg.Gates["hop"].BudgetPct; got == nil || *got != 0 {
		t.Fatalf("gates.hop.budget_pct = %v, want 0 (explicit)", got)
	}
	if _, ok := cfg.Gates["perf"]; ok {
		t.Fatalf("gates.perf must stay absent when not configured, got %+v", cfg.Gates)
	}
	if len(cfg.FailOn) != 2 || cfg.FailOn[0] != "pixel" || cfg.FailOn[1] != "wire" {
		t.Fatalf("fail_on = %+v, want [pixel wire]", cfg.FailOn)
	}
}

// TestRectWhyFieldRoundTripsThroughYamlAndJson is the item-2 text test:
// Rect.Why must parse from real YAML and, separately, marshal to the
// "why" JSON key (with omitempty when blank).
func TestRectWhyFieldRoundTripsThroughYamlAndJson(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "retrace.yaml")
	yamlSrc := "app: web\n" +
		"masks:\n" +
		"  cart: [{ x: 10, y: 20, width: 100, height: 40, why: \"flaky ad slot\" }]\n"
	os.WriteFile(path, []byte(yamlSrc), 0o644)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	rects := cfg.Masks["cart"]
	if len(rects) != 1 || rects[0].Why != "flaky ad slot" {
		t.Fatalf("masks.cart[0].why = %+v, want \"flaky ad slot\"", rects)
	}

	b, err := json.Marshal(rects[0])
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if !strings.Contains(string(b), `"why":"flaky ad slot"`) {
		t.Fatalf("marshaled Rect must carry why, got %s", b)
	}

	blank, err := json.Marshal(Rect{X: 1, Y: 1, Width: 2, Height: 2})
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if strings.Contains(string(blank), "why") {
		t.Fatalf("a blank Why must be omitted from JSON (omitempty), got %s", blank)
	}
}

// TestWireIgnoreAcceptsBareStringAndObjectForm is the item-3 text test:
// both YAML shapes must parse into the same []WireIgnoreEntry, and the bare
// form (every existing config's shape) must keep working.
func TestWireIgnoreAcceptsBareStringAndObjectForm(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "retrace.yaml")
	yamlSrc := "app: web\n" +
		"wire_ignore:\n" +
		"  - \"date\"\n" +
		"  - path: \"/health\"\n" +
		"    why: \"polled by the load balancer\"\n"
	os.WriteFile(path, []byte(yamlSrc), 0o644)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.WireIgnore) != 2 {
		t.Fatalf("wire_ignore = %+v, want 2 entries", cfg.WireIgnore)
	}
	if cfg.WireIgnore[0].Path != "date" || cfg.WireIgnore[0].Why != "" {
		t.Fatalf("bare-scalar wire_ignore entry = %+v, want Path=date Why=\"\"", cfg.WireIgnore[0])
	}
	if cfg.WireIgnore[1].Path != "/health" || cfg.WireIgnore[1].Why != "polled by the load balancer" {
		t.Fatalf("object-form wire_ignore entry = %+v", cfg.WireIgnore[1])
	}
}

// TestPreflightSetupTeardownParseOnConfigAndFlow is the item-4 text test:
// shape only, parsed and tagged, not executed.
func TestPreflightSetupTeardownParseOnConfigAndFlow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "retrace.yaml")
	yamlSrc := "app: web\n" +
		"preflight: [\"npm run db:seed\"]\n" +
		"flows:\n" +
		"  checkout:\n" +
		"    command: npx playwright test checkout.spec.ts\n" +
		"    preflight: [\"npm run flow:seed\"]\n" +
		"    setup: [\"npm run flow:setup\"]\n" +
		"    teardown: [\"npm run flow:teardown\"]\n"
	os.WriteFile(path, []byte(yamlSrc), 0o644)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Preflight) != 1 || cfg.Preflight[0] != "npm run db:seed" {
		t.Fatalf("Config.Preflight = %+v", cfg.Preflight)
	}
	flow := cfg.Flows["checkout"]
	if len(flow.Preflight) != 1 || flow.Preflight[0] != "npm run flow:seed" {
		t.Fatalf("Flow.Preflight = %+v", flow.Preflight)
	}
	if len(flow.Setup) != 1 || flow.Setup[0] != "npm run flow:setup" {
		t.Fatalf("Flow.Setup = %+v", flow.Setup)
	}
	if len(flow.Teardown) != 1 || flow.Teardown[0] != "npm run flow:teardown" {
		t.Fatalf("Flow.Teardown = %+v", flow.Teardown)
	}
}

// TestABadPathNormalizeRegexFailsLoadNamingIt pins MINOR finding 8's M17:
// a typo'd path_normalize pattern must be a hard Load error, not a silent
// no-op that leaves every later diff unnormalized.
func TestABadPathNormalizeRegexFailsLoadNamingIt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "retrace.yaml")
	os.WriteFile(path, []byte("path_normalize:\n  - { pattern: \"(unclosed\", replacement: \"x\" }\n"), 0o644)

	_, err := Load(path)
	if err == nil {
		t.Fatal("a bad path_normalize regex must fail Load")
	}
	if !strings.Contains(err.Error(), "path_normalize[0]") {
		t.Fatalf("error must name the offending pattern, got: %v", err)
	}
}

// TestZeroValueNormalizeApplyIsANoOp pins MINOR finding 8's M16: Apply's
// nil-regex guard, which its doc comment specifically promises for a
// hand-built or zero-value Normalize, must not panic.
func TestZeroValueNormalizeApplyIsANoOp(t *testing.T) {
	var n Normalize
	if got := n.Apply("/users/42"); got != "/users/42" {
		t.Fatalf("a zero-value Normalize.Apply must be a no-op, got %q", got)
	}
}

// TestDiscoverValidatesRulesAfterMergingOverlay pins M8 from the review's
// mutation ledger (not individually named in finding 4, but part of the
// same "zero survivors" bar): dropping Discover's post-merge c.Rules()
// validation would let a hand-edited overlay (bypassing AppendWireRule's
// own validation) silently reach Discover with an unusable rule instead of
// failing loudly.
func TestDiscoverValidatesRulesAfterMergingOverlay(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "retrace.yaml"), []byte("app: web\n"), 0o644)
	overlayDir := filepath.Join(dir, ".retrace")
	os.MkdirAll(overlayDir, 0o755)
	// Hand-written, not through AppendWireRule — an unknown matcher name.
	os.WriteFile(filepath.Join(overlayDir, "wire-rules.json"), []byte(`[{"path":"/cart","headers":{"date":"httpdate"}}]`), 0o644)

	_, err := Discover(dir)
	if err == nil {
		t.Fatal("Discover must validate rules after merging the overlay, not just after loading yaml")
	}
	if !strings.Contains(err.Error(), "http-date") {
		t.Fatalf("error should suggest the real matcher name, got: %v", err)
	}
}

// TestMalformedOverlayJSONFailsDiscoverNamingThePath pins MINOR finding 8's
// M19: a syntactically broken overlay must be a named error, not silently
// ignored.
func TestMalformedOverlayJSONFailsDiscoverNamingThePath(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "retrace.yaml"), []byte("app: web\n"), 0o644)
	overlayDir := filepath.Join(dir, ".retrace")
	os.MkdirAll(overlayDir, 0o755)
	os.WriteFile(filepath.Join(overlayDir, "wire-rules.json"), []byte(`not json`), 0o644)

	_, err := Discover(dir)
	if err == nil {
		t.Fatal("malformed overlay JSON must fail Discover")
	}
	if !strings.Contains(err.Error(), "wire-rules.json") {
		t.Fatalf("error must name the overlay path, got: %v", err)
	}
}
