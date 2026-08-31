package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
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

// proxy_host is empty by default (the built-in "127.0.0.1" default lives in
// retrace/capture, not here — see design.md §6.1.2), and round-trips when set.
func TestProxyHostDefaultsEmptyAndParsesWhenSet(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "retrace.yaml"), []byte("app: web\n"), 0o644)
	cfg, err := Load(filepath.Join(dir, "retrace.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ProxyHost != "" {
		t.Fatalf("ProxyHost = %q, want empty when unset in retrace.yaml", cfg.ProxyHost)
	}

	os.WriteFile(filepath.Join(dir, "retrace.yaml"), []byte("app: web\nproxy_host: localhost\n"), 0o644)
	cfg, err = Load(filepath.Join(dir, "retrace.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ProxyHost != "localhost" {
		t.Fatalf("ProxyHost = %q, want %q", cfg.ProxyHost, "localhost")
	}
}

// proxy_port is zero by default (the built-in ephemeral-port default lives
// in retrace/capture, not here — see design.md §6.1.2's proxy.port
// addendum), and round-trips when set.
func TestProxyPortDefaultsZeroAndParsesWhenSet(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "retrace.yaml"), []byte("app: web\n"), 0o644)
	cfg, err := Load(filepath.Join(dir, "retrace.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ProxyPort != 0 {
		t.Fatalf("ProxyPort = %d, want 0 when unset in retrace.yaml", cfg.ProxyPort)
	}

	os.WriteFile(filepath.Join(dir, "retrace.yaml"), []byte("app: web\nproxy_port: 4000\n"), 0o644)
	cfg, err = Load(filepath.Join(dir, "retrace.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ProxyPort != 4000 {
		t.Fatalf("ProxyPort = %d, want %d", cfg.ProxyPort, 4000)
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

// TestThresholdsGateAboveOneFailsLoadNamingTheKey pins F6: thresholds.gate
// is overloaded between pixel.Match's per-pixel colour-distance threshold
// and summary.go's percent-of-pixels comparison. They coincide near the 0.1
// default and diverge completely at gate >= 1, where every checkpoint
// silently reports 0.00% forever (the pixel plane is permanently green). A
// user writing `gate: 5` meaning "5% of pixels may differ" is the plausible
// mistake this guards against, not an arbitrary out-of-range number.
func TestThresholdsGateAboveOneFailsLoadNamingTheKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "retrace.yaml")
	os.WriteFile(path, []byte("app: web\nthresholds:\n  gate: 5\n"), 0o644)

	_, err := Load(path)
	if err == nil {
		t.Fatal("thresholds.gate: 5 must fail Load, not silently ship a gate that never fires")
	}
	if !strings.Contains(err.Error(), "thresholds.gate") {
		t.Fatalf("error must name the offending key, got: %v", err)
	}
}

// TestThresholdsFineAboveOneFailsLoadNamingTheKey mutates the other arm of
// TestThresholdsGateAboveOneFailsLoadNamingTheKey (fine, not gate) — per the
// global constraints' fixture-symmetry rule, a guard proven only on Gate
// could still have a Fine copy that drifted or was never wired up.
func TestThresholdsFineAboveOneFailsLoadNamingTheKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "retrace.yaml")
	os.WriteFile(path, []byte("app: web\nthresholds:\n  fine: 5\n"), 0o644)

	_, err := Load(path)
	if err == nil {
		t.Fatal("thresholds.fine: 5 must fail Load, not silently ship a threshold that never fires")
	}
	if !strings.Contains(err.Error(), "thresholds.fine") {
		t.Fatalf("error must name the offending key, got: %v", err)
	}
}

// TestThresholdsGateBoundaryIsExclusiveOfOne pins the boundary in both
// directions: 0.99 is a valid fraction and must load; 1 is out of the open
// interval (0, 1) and must not.
func TestThresholdsGateBoundaryIsExclusiveOfOne(t *testing.T) {
	dir := t.TempDir()

	okPath := filepath.Join(dir, "ok.yaml")
	os.WriteFile(okPath, []byte("app: web\nthresholds:\n  gate: 0.99\n"), 0o644)
	cfg, err := Load(okPath)
	if err != nil {
		t.Fatalf("thresholds.gate: 0.99 must load, got error: %v", err)
	}
	if cfg.Thresholds.Gate != 0.99 {
		t.Fatalf("Thresholds.Gate = %v, want 0.99", cfg.Thresholds.Gate)
	}

	badPath := filepath.Join(dir, "bad.yaml")
	os.WriteFile(badPath, []byte("app: web\nthresholds:\n  gate: 1\n"), 0o644)
	if _, err := Load(badPath); err == nil {
		t.Fatal("thresholds.gate: 1 must fail Load — the open interval (0, 1) excludes 1")
	}
}

// TestThresholdsGateOmittedStillLoadsAndDefaults pins that the new guard
// does not disturb the existing zero-means-unset behavior: a retrace.yaml
// with no thresholds: block at all must still load cleanly and yield
// DefaultGate/DefaultFine, not an error from the new range check seeing a
// zero value.
func TestThresholdsGateOmittedStillLoadsAndDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "retrace.yaml")
	os.WriteFile(path, []byte("app: web\n"), 0o644)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load with no thresholds: block must succeed, got: %v", err)
	}
	if cfg.Thresholds.Gate != DefaultGate {
		t.Fatalf("Thresholds.Gate = %v, want DefaultGate (%v)", cfg.Thresholds.Gate, DefaultGate)
	}
	if cfg.Thresholds.Fine != DefaultFine {
		t.Fatalf("Thresholds.Fine = %v, want DefaultFine (%v)", cfg.Thresholds.Fine, DefaultFine)
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

	// A non-default thresholds.gate, with no gates: block at all, must still
	// flow through to gates.pixel — pins F6/M5: replacing
	// `gate := c.Thresholds.Gate` with `gate := DefaultGate` left the whole
	// suite green because every other case used the default thresholds,
	// where the two values are numerically identical (0.1 == 0.1).
	path3 := filepath.Join(dir, "retrace3.yaml")
	os.WriteFile(path3, []byte("app: web\nthresholds:\n  gate: 0.5\n"), 0o644)
	cfg3, err := Load(path3)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	g3, ok := cfg3.Gates["pixel"]
	if !ok || g3.BudgetPct == nil {
		t.Fatalf("gates.pixel must be present and set when absent from config, got %+v", cfg3.Gates)
	}
	if *g3.BudgetPct != 0.5 {
		t.Fatalf("gates.pixel.budget_pct with thresholds.gate=0.5 and no gates: block = %v, want 0.5", *g3.BudgetPct)
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
		"  - path: \"items[*].requestId\"\n" +
		"    why: \"regenerated on every request\"\n"
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
	if cfg.WireIgnore[1].Path != "items[*].requestId" || cfg.WireIgnore[1].Why != "regenerated on every request" {
		t.Fatalf("object-form wire_ignore entry = %+v", cfg.WireIgnore[1])
	}
}

// TestWireIgnoreRejectsAURLPathEntry pins F13: config.go's own doc comment
// used to give "/health" — a URL path — as the WireIgnoreEntry example,
// under semantics where wire_ignore matches body field-path globs. A user
// following that example got a mask that silently matched nothing. Load
// must now reject any wire_ignore entry whose Path starts with "/", naming
// the offender.
func TestWireIgnoreRejectsAURLPathEntry(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "retrace.yaml")
	yamlSrc := "app: web\n" +
		"wire_ignore:\n" +
		"  - path: \"/health\"\n" +
		"    why: \"polled by the load balancer\"\n"
	os.WriteFile(path, []byte(yamlSrc), 0o644)

	_, err := Load(path)
	if err == nil {
		t.Fatal("a wire_ignore entry starting with '/' must fail Load, not silently ship a mask that matches nothing")
	}
	if !strings.Contains(err.Error(), "/health") {
		t.Fatalf("error must name the offending path, got: %v", err)
	}
}

// TestWireIgnoreObjectFormRejectsATypoedWhyKey pins F1: a custom
// UnmarshalYAML that calls node.Decode gets a fresh decoder with
// KnownFields off, so the outer decoder's strictness does not propagate.
// Before the fix, "whyy:" was silently accepted and dropped — precisely the
// field whose whole purpose is to carry the reason for the ignore. Mutating
// UnmarshalYAML back to a bare node.Decode(&p) (removing the manual
// known-fields walk) must make this fail.
func TestWireIgnoreObjectFormRejectsATypoedWhyKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "retrace.yaml")
	yamlSrc := "app: web\n" +
		"wire_ignore:\n" +
		"  - path: \"items[*].requestId\"\n" +
		"    whyy: \"typo\"\n"
	os.WriteFile(path, []byte(yamlSrc), 0o644)

	_, err := Load(path)
	if err == nil {
		t.Fatal("a typo'd wire_ignore object key must fail Load, not silently drop the field")
	}
	if !strings.Contains(err.Error(), "whyy") {
		t.Fatalf("error must name the offending key, got: %v", err)
	}
	if !strings.Contains(err.Error(), "config.WireIgnoreEntry") {
		t.Fatalf("error must name the type, matching this package's house style, got: %v", err)
	}
}

// TestWireIgnorePathsDropsEmptyPathEntries pins F2: Config.WireIgnorePaths
// must exist for Task 10, and an entry that parsed to Path == "" must be
// dropped rather than passed down — an empty path reaching the diff engine
// as an ignore rule would match everything, the most permissive value the
// type has.
func TestWireIgnorePathsDropsEmptyPathEntries(t *testing.T) {
	c := &Config{WireIgnore: []WireIgnoreEntry{
		{Path: "/health", Why: "polled by the load balancer"},
		{Path: "", Why: "malformed entry, no path"},
		{Path: "date"},
	}}
	got := c.WireIgnorePaths()
	want := []string{"/health", "date"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("WireIgnorePaths() = %+v, want %+v (empty Path dropped)", got, want)
	}
}

// TestUnknownGatePlaneNameFailsLoadNamingIt pins F5: gates: is a
// map[string]Gate, so a typo'd plane name escapes KnownFields entirely —
// "pixle" would load clean and silently leave the intended "pixel" plane at
// its default while gating nothing.
func TestUnknownGatePlaneNameFailsLoadNamingIt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "retrace.yaml")
	os.WriteFile(path, []byte("app: web\ngates:\n  pixle:\n    budget_pct: 0\n"), 0o644)

	_, err := Load(path)
	if err == nil {
		t.Fatal("an unknown gates plane name must fail Load")
	}
	if !strings.Contains(err.Error(), "pixle") {
		t.Fatalf("error must name the offending plane, got: %v", err)
	}
}

// TestUnknownFailOnPlaneNameFailsLoadNamingIt pins the fail_on half of F5.
func TestUnknownFailOnPlaneNameFailsLoadNamingIt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "retrace.yaml")
	os.WriteFile(path, []byte("app: web\nfail_on: [\"pixle\", \"nonsense\"]\n"), 0o644)

	_, err := Load(path)
	if err == nil {
		t.Fatal("an unknown fail_on plane name must fail Load")
	}
	if !strings.Contains(err.Error(), "pixle") {
		t.Fatalf("error must name the offending plane, got: %v", err)
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

// TestTheTwoMaskEnumerationsStaySeparate pins the enumeration
// `retrace ref accept` needs to detect a misspelt mask entry. MasksFor
// cannot do it: a lookup returns nil for a name it does not hold, which is
// indistinguishable from "this screen needs no mask", so the promotion
// copies the shot through unredacted and exits 0.
//
// The two enumerations carry DIFFERENT verdicts and so must not merge. A
// flow-scoped entry can only ever apply to its own flow, so one matching
// nothing in the run is protecting nothing anywhere and Accept refuses it.
// A top-level entry applies to every flow, so one matching nothing HERE is
// very likely doing its job in another flow — Accept only reports it.
// Merging the two maps is what makes a correct multi-flow config refuse,
// which is why that merge is what this test exists to kill.
func TestTheTwoMaskEnumerationsStaySeparate(t *testing.T) {
	c := &Config{
		Masks: map[string][]Rect{"*": {{Width: 1}}, "cart": {{Width: 2}}},
		Flows: map[string]Flow{
			"checkout": {Masks: map[string][]Rect{"receipt": {{Width: 3}}, "cart": {{Width: 4}}}},
			"login":    {Masks: map[string][]Rect{"password": {{Width: 5}}}},
		},
	}

	// The flow's OWN map only. "cart" is here because checkout declares it,
	// not because the top-level map does — "receipt" is the half that
	// proves it, and the absence of nothing-from-top-level is proved by the
	// login case below.
	if got := strings.Join(c.FlowMaskEntryCheckpoints("checkout"), ","); got != "cart,receipt" {
		t.Fatalf("FlowMaskEntryCheckpoints(checkout) = %q, want %q — this flow's own map, sorted", got, "cart,receipt")
	}
	// login declares only "password". If the top-level map were folded in,
	// "cart" would appear here — and a promotion of login would then refuse
	// over an entry that is protecting checkout's screen perfectly well.
	if got := strings.Join(c.FlowMaskEntryCheckpoints("login"), ","); got != "password" {
		t.Fatalf("FlowMaskEntryCheckpoints(login) = %q, want %q — the top-level map must NOT be folded into a flow's own scope", got, "password")
	}
	// Another flow's entries never leak sideways.
	if got := strings.Join(c.FlowMaskEntryCheckpoints("checkout"), ","); strings.Contains(got, "password") {
		t.Fatalf("FlowMaskEntryCheckpoints(checkout) = %q — login's entries belong to another flow's scope", got)
	}

	// The project-wide map, without the wildcard: "*" names no checkpoint,
	// so it can never be a typo and must never be reported as unmatched.
	if got := strings.Join(c.ProjectMaskEntryCheckpoints(), ","); got != "cart" {
		t.Fatalf("ProjectMaskEntryCheckpoints() = %q, want %q — top-level only, without the wildcard", got, "cart")
	}

	// An empty config invents nothing. An invented entry would refuse a
	// correct promotion.
	if got := len((&Config{}).FlowMaskEntryCheckpoints("checkout")); got != 0 {
		t.Fatalf("a config with no masks declared %d flow entries, want 0", got)
	}
	if got := len((&Config{}).ProjectMaskEntryCheckpoints()); got != 0 {
		t.Fatalf("a config with no masks declared %d project entries, want 0", got)
	}
}

// TestConcurrentGoroutinesWithNoFileLockLandEveryRule is the WINDOWS
// configuration, run on this machine: lockOverlayFn stubbed to a no-op is
// exactly what overlaylock_other.go compiles to, and there overlayMu is the
// only serialization AppendWireRule has left.
//
// It is deliberately NOT a duplicate of
// TestNSeparateProcessesAppendingConcurrentlyLandNRules, and the two must
// not be merged. That test measures the flock and SURVIVES the removal of
// overlayMu — which is the control proving it is not secretly testing the
// mutex. This test is the other half: it dies when overlayMu is removed and
// says nothing about the flock. Deleting either one leaves a real platform
// unprotected with a green suite.
func TestConcurrentGoroutinesWithNoFileLockLandEveryRule(t *testing.T) {
	real := lockOverlayFn
	lockOverlayFn = func(string) (func(), error) { return func() {}, nil }
	t.Cleanup(func() { lockOverlayFn = real })

	dir := t.TempDir()
	const goroutines, per = 8, 12

	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make([]error, goroutines)
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			<-start // one starting gun, so every goroutine contends at once
			for i := 0; i < per; i++ {
				r := rules.Raw{Path: "/g" + strconv.Itoa(g) + "/rule" + strconv.Itoa(i), Headers: map[string]any{"date": "http-date"}}
				if err := AppendWireRule(dir, r); err != nil {
					errs[g] = err
					return
				}
			}
		}(g)
	}
	close(start)
	wg.Wait()
	for g, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: AppendWireRule: %v", g, err)
		}
	}

	got, err := readOverlay(filepath.Join(dir, OverlayPath))
	if err != nil {
		t.Fatalf("readOverlay: %v", err)
	}
	// Every rule is DISTINCT, so the idempotent dedupe cannot account for a
	// shortfall: a missing rule was lost by a read-modify-write race, and
	// every lost append returned a nil error.
	if len(got) != goroutines*per {
		t.Fatalf("overlay holds %d rules, want %d — %d appends were silently lost between goroutines, each returning nil; on a platform with no file lock overlayMu is the ONLY thing that prevents this",
			len(got), goroutines*per, goroutines*per-len(got))
	}
}

// TestQueryIgnoreIsATopLevelKeyAndBlankEntriesAreDropped pins R-J's config
// half. `query_ignore` is a real key (KnownFields(true) makes a typo an
// error, so a config carrying it only loads if the field exists), it is
// PROJECT-WIDE and top-level like its sibling `wire_ignore`, and a blank
// entry never reaches the matcher: an empty key is the most permissive
// value the type has, and the zero-value constraint says an unset value
// must not become a permissive one. The other half — that the key changes
// what `retrace replay` actually matches — is pinned at the CLI seam by
// TestRetraceYamlDecidesWhatReplayMatches.
func TestQueryIgnoreIsATopLevelKeyAndBlankEntriesAreDropped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "retrace.yaml")
	os.WriteFile(path, []byte("app: web\nquery_ignore:\n  - t\n  - \"\"\n  - \"   \"\n  - cb\n"), 0o644)

	c, err := Load(path)
	if err != nil {
		t.Fatalf("query_ignore did not load as a top-level key: %v", err)
	}
	got := c.QueryIgnoreKeys()
	if len(got) != 2 || got[0] != "t" || got[1] != "cb" {
		t.Fatalf("QueryIgnoreKeys() = %q, want the two named params with the blanks dropped", got)
	}
	// The mirror: no key at all yields no ignores, never a nil-derived
	// "ignore everything".
	empty := &Config{}
	if len(empty.QueryIgnoreKeys()) != 0 {
		t.Fatalf("QueryIgnoreKeys() on a config with no query_ignore = %q, want none", empty.QueryIgnoreKeys())
	}
}

func TestRectPctFieldRoundTripsThroughYaml(t *testing.T) {
	dir := t.TempDir()
	yaml := `
masks:
  "*": [{ height: 0.06, width: 1, x: 0, y: 0, pct: true, why: "status bar" }]
`
	path := filepath.Join(dir, "retrace.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatalf("writing retrace.yaml: %v", err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := c.MasksFor("cart", "anything")
	if len(got) != 1 {
		t.Fatalf("MasksFor(...) = %+v, want one rect", got)
	}
	want := Rect{X: 0, Y: 0, Width: 1, Height: 0.06, Pct: true, Why: "status bar"}
	if got[0] != want {
		t.Fatalf("MasksFor(...)[0] = %+v, want %+v", got[0], want)
	}
}

func TestLoadRejectsAnOutOfRangePctRect(t *testing.T) {
	dir := t.TempDir()
	yaml := `
masks:
  "*": [{ height: 1.5, width: 1, x: 0, y: 0, pct: true }]
`
	path := filepath.Join(dir, "retrace.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatalf("writing retrace.yaml: %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load accepted a pct rect with height 1.5 — a fraction cannot exceed 1")
	}
	if !strings.Contains(err.Error(), "height") || !strings.Contains(err.Error(), "[0,1]") {
		t.Fatalf("error = %q, want it to name the field and the [0,1] bound", err.Error())
	}
}

func TestLoadAcceptsAbsolutePctFalseRectsUnbounded(t *testing.T) {
	// A non-pct rect is still allowed to name pixel coordinates far past 1
	// — that is the overwhelmingly common case (e.g. width: 300) and this
	// pins that the new [0,1] bound applies ONLY to pct: true rects.
	dir := t.TempDir()
	yaml := `
masks:
  "*": [{ x: 10, y: 20, width: 300, height: 40 }]
`
	path := filepath.Join(dir, "retrace.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatalf("writing retrace.yaml: %v", err)
	}
	if _, err := Load(path); err != nil {
		t.Fatalf("Load rejected an ordinary absolute-pixel mask: %v", err)
	}
}
