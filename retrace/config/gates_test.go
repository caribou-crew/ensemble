package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func pct(v float64) *float64 { return &v }

func TestAFlowWithNoOverridesGetsTheGlobalGates(t *testing.T) {
	c := &Config{Gates: map[string]Gate{"pixel": {BudgetPct: pct(1.5)}}}
	got := c.ResolveGates("checkout")
	if g := got["pixel"]; g.BudgetPct == nil || *g.BudgetPct != 1.5 {
		t.Fatalf("pixel = %+v, want budget_pct 1.5", g)
	}
}

func TestAnUnknownFlowResolvesToTheGlobalGatesRatherThanNothing(t *testing.T) {
	// `retrace diff --flow x` against a config with no `flows:` block is an
	// ordinary thing to do. Returning an empty map would silently ungate every
	// plane, which is a passing build over an unconfigured comparison.
	c := &Config{Gates: map[string]Gate{"wire": {BudgetPct: pct(2)}}}
	for _, flow := range []string{"", "nonexistent"} {
		got := c.ResolveGates(flow)
		if g := got["wire"]; g.BudgetPct == nil || *g.BudgetPct != 2 {
			t.Errorf("ResolveGates(%q) wire = %+v, want the global budget", flow, g)
		}
	}
}

func TestAFlowOverridesOnlyThePlaneItNames(t *testing.T) {
	c := &Config{
		Gates: map[string]Gate{
			"pixel": {BudgetPct: pct(1.5)},
			"wire":  {BudgetPct: pct(2)},
		},
		Flows: map[string]Flow{
			"checkout": {Gates: map[string]Gate{"pixel": {BudgetPct: pct(5)}}},
		},
	}
	got := c.ResolveGates("checkout")
	if g := got["pixel"]; g.BudgetPct == nil || *g.BudgetPct != 5 {
		t.Errorf("pixel = %+v, want the flow's 5", g)
	}
	if g := got["wire"]; g.BudgetPct == nil || *g.BudgetPct != 2 {
		t.Errorf("wire = %+v, want the global 2 — a flow that overrides pixel must not disturb wire", g)
	}
}

func TestAFlowCanGateAPlaneTheGlobalConfigDoesNot(t *testing.T) {
	// The override is a merge, not a filter. A flow that is the only one with
	// a perf budget must actually get one.
	c := &Config{
		Gates: map[string]Gate{"pixel": {BudgetPct: pct(1.5)}},
		Flows: map[string]Flow{"slow": {Gates: map[string]Gate{"perf": {BudgetPct: pct(10)}}}},
	}
	if g := c.ResolveGates("slow")["perf"]; g.BudgetPct == nil || *g.BudgetPct != 10 {
		t.Errorf("perf = %+v, want the flow's 10", g)
	}
	if _, ok := c.ResolveGates("other")["perf"]; ok {
		t.Error("another flow picked up perf — a per-flow gate must not leak")
	}
}

func TestAFlowBudgetDoesNotDiscardGlobalCheckpointOverrides(t *testing.T) {
	// The reason the merge is per-field. Wholesale replacement means a flow
	// that WIDENS its overall budget to 5 silently TIGHTENS the cart screen
	// from its declared 8 down to 5 — a knob that changes something it does
	// not name, in the opposite direction from the one it was turned.
	c := &Config{
		Gates: map[string]Gate{"pixel": {BudgetPct: pct(1.5), Checkpoints: map[string]float64{"cart": 8}}},
		Flows: map[string]Flow{"checkout": {Gates: map[string]Gate{"pixel": {BudgetPct: pct(5)}}}},
	}
	g := c.ResolveGates("checkout")["pixel"]
	if g.BudgetPct == nil || *g.BudgetPct != 5 {
		t.Errorf("budget_pct = %+v, want the flow's 5", g.BudgetPct)
	}
	if got := g.Checkpoints["cart"]; got != 8 {
		t.Errorf("cart = %v, want the global 8 — a flow's budget_pct must not discard checkpoint overrides", got)
	}
}

func TestAFlowCheckpointOverrideDoesNotDiscardTheGlobalBudget(t *testing.T) {
	// The mirror image of the test above, and the mutation that survives if
	// mergeGate copies only in one direction.
	c := &Config{
		Gates: map[string]Gate{"pixel": {BudgetPct: pct(1.5)}},
		Flows: map[string]Flow{"checkout": {Gates: map[string]Gate{"pixel": {Checkpoints: map[string]float64{"cart": 8}}}}},
	}
	g := c.ResolveGates("checkout")["pixel"]
	if g.BudgetPct == nil || *g.BudgetPct != 1.5 {
		t.Errorf("budget_pct = %+v, want the global 1.5 to survive a checkpoint-only override", g.BudgetPct)
	}
	if got := g.Checkpoints["cart"]; got != 8 {
		t.Errorf("cart = %v, want 8", got)
	}
}

func TestCheckpointOverridesMergePerName(t *testing.T) {
	c := &Config{
		Gates: map[string]Gate{"pixel": {BudgetPct: pct(1), Checkpoints: map[string]float64{"cart": 8, "login": 2}}},
		Flows: map[string]Flow{"checkout": {Gates: map[string]Gate{"pixel": {Checkpoints: map[string]float64{"login": 6, "receipt": 4}}}}},
	}
	g := c.ResolveGates("checkout")["pixel"]
	for name, want := range map[string]float64{"cart": 8, "login": 6, "receipt": 4} {
		if got := g.Checkpoints[name]; got != want {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}
}

func TestResolveGatesNeverHandsBackTheConfigsOwnMaps(t *testing.T) {
	// `retrace run` compares several flows in one process. If a resolved map
	// aliased the config, the first flow's resolution would re-budget every
	// flow after it — and the aliasing is silent until it isn't.
	c := &Config{Gates: map[string]Gate{"pixel": {BudgetPct: pct(1.5), Checkpoints: map[string]float64{"cart": 8}}}}

	got := c.ResolveGates("a")
	*got["pixel"].BudgetPct = 99
	got["pixel"].Checkpoints["cart"] = 99
	got["wire"] = Gate{BudgetPct: pct(3)}

	if g := c.Gates["pixel"]; *g.BudgetPct != 1.5 {
		t.Errorf("the config's own budget_pct changed to %v", *g.BudgetPct)
	}
	if got := c.Gates["pixel"].Checkpoints["cart"]; got != 8 {
		t.Errorf("the config's own checkpoint budget changed to %v", got)
	}
	if _, ok := c.Gates["wire"]; ok {
		t.Error("a plane added to a resolved map appeared in the config")
	}
	if g := c.ResolveGates("b")["pixel"]; *g.BudgetPct != 1.5 {
		t.Errorf("a second resolution saw %v — the first one's mutation leaked", *g.BudgetPct)
	}
}

func TestAFlowOverrideNeverAliasesTheFlowsOwnMap(t *testing.T) {
	// Same hazard, the other source map. mergeGate copies base and then writes
	// the override's values in; writing them into the FLOW's map instead would
	// make the flow's config drift with every resolution.
	c := &Config{
		Gates: map[string]Gate{"pixel": {BudgetPct: pct(1)}},
		Flows: map[string]Flow{"checkout": {Gates: map[string]Gate{"pixel": {Checkpoints: map[string]float64{"cart": 8}}}}},
	}
	got := c.ResolveGates("checkout")
	got["pixel"].Checkpoints["cart"] = 99
	if v := c.Flows["checkout"].Gates["pixel"].Checkpoints["cart"]; v != 8 {
		t.Errorf("the flow's own checkpoint budget changed to %v", v)
	}
}

// --- BudgetFor ------------------------------------------------------------

func TestBudgetForPrefersTheCheckpointOverride(t *testing.T) {
	g := Gate{BudgetPct: pct(1.5), Checkpoints: map[string]float64{"cart": 8}}

	budget, per, ok := g.BudgetFor("cart")
	if !ok || budget != 8 || !per {
		t.Errorf("cart = (%v, %v, %v), want (8, true, true)", budget, per, ok)
	}
	budget, per, ok = g.BudgetFor("login")
	if !ok || budget != 1.5 || per {
		t.Errorf("login = (%v, %v, %v), want (1.5, false, true)", budget, per, ok)
	}
}

func TestAnExplicitZeroCheckpointBudgetIsARealSetting(t *testing.T) {
	// "This screen must not move at all" is the strictest gate there is and
	// has to be expressible. A map key's presence draws the distinction that
	// budget_pct needs a pointer for.
	g := Gate{BudgetPct: pct(5), Checkpoints: map[string]float64{"login": 0}}
	budget, per, ok := g.BudgetFor("login")
	if !ok || budget != 0 || !per {
		t.Errorf("login = (%v, %v, %v), want (0, true, true) — an explicit 0 must not read as absent", budget, per, ok)
	}
}

func TestBudgetForRefusesRatherThanInventingAZeroBudget(t *testing.T) {
	// The zero-value trap: a plane with no budget at all must report "no
	// budget", not 0. A 0 read as a budget is the STRICTEST possible gate,
	// arrived at by accident, failing a build over a change nobody gated.
	g := Gate{}
	if budget, _, ok := g.BudgetFor("cart"); ok {
		t.Errorf("an unbudgeted plane reported a budget of %v", budget)
	}
}

// --- validation -----------------------------------------------------------

func TestCheckpointsOnANonPixelPlaneIsAConfigError(t *testing.T) {
	// Left unchecked this loads clean and does nothing — the user believes a
	// budget is in force and no error anywhere says otherwise.
	for _, plane := range []string{"wire", "hop", "perf"} {
		c := &Config{Gates: map[string]Gate{plane: {BudgetPct: pct(1), Checkpoints: map[string]float64{"cart": 8}}}}
		err := validatePlanes(c)
		if err == nil {
			t.Errorf("gates.%s.checkpoints loaded clean", plane)
			continue
		}
		if !strings.Contains(err.Error(), plane) {
			t.Errorf("the error does not name the offending plane: %v", err)
		}
	}
}

func TestCheckpointsOnPixelIsFine(t *testing.T) {
	c := &Config{Gates: map[string]Gate{"pixel": {BudgetPct: pct(1), Checkpoints: map[string]float64{"cart": 8}}}}
	if err := validatePlanes(c); err != nil {
		t.Fatalf("gates.pixel.checkpoints rejected: %v", err)
	}
}

func TestAFlowsGatesGetTheSameValidationAsTheGlobalOnes(t *testing.T) {
	// A typo caught at the top level and waved through one level down is
	// worse than one caught nowhere: the correctly-spelled plane sits right
	// above it, so the file reads as though it works.
	bad := &Config{Flows: map[string]Flow{"checkout": {Gates: map[string]Gate{"pixle": {BudgetPct: pct(1)}}}}}
	err := validatePlanes(bad)
	if err == nil {
		t.Fatal("flows.checkout.gates.pixle loaded clean")
	}
	if !strings.Contains(err.Error(), "checkout") || !strings.Contains(err.Error(), "pixle") {
		t.Errorf("the error must name both the flow and the typo: %v", err)
	}

	badCP := &Config{Flows: map[string]Flow{"checkout": {Gates: map[string]Gate{"wire": {Checkpoints: map[string]float64{"cart": 1}}}}}}
	if err := validatePlanes(badCP); err == nil {
		t.Fatal("flows.checkout.gates.wire.checkpoints loaded clean")
	} else if !strings.Contains(err.Error(), "checkout") {
		t.Errorf("the error must name the flow: %v", err)
	}
}

func TestTwoBadPlanesReportTheSameOffenderOnEveryRun(t *testing.T) {
	// The plane-level half of the ordering, which the flow-level test below
	// cannot reach: sortedPlanes orders the planes WITHIN one gates map, and a
	// config with one bad plane per flow exercises only sortedFlowNames.
	c := &Config{Gates: map[string]Gate{
		"wire": {BudgetPct: pct(1), Checkpoints: map[string]float64{"cart": 1}},
		"hop":  {BudgetPct: pct(1), Checkpoints: map[string]float64{"cart": 1}},
	}}
	first := validatePlanes(c).Error()
	for i := 0; i < 20; i++ {
		if got := validatePlanes(c).Error(); got != first {
			t.Fatalf("run %d reported %q, first run reported %q", i, got, first)
		}
	}
	if !strings.Contains(first, "hop") {
		t.Errorf("the ordering is not alphabetical by plane: %q", first)
	}
}

func TestTwoBadFlowsReportTheSameOffenderOnEveryRun(t *testing.T) {
	// Go randomizes map iteration. Without an ordering, a user fixing the
	// error they were shown sees it "move" to the other flow.
	c := &Config{Flows: map[string]Flow{
		"zeta":  {Gates: map[string]Gate{"nope": {BudgetPct: pct(1)}}},
		"alpha": {Gates: map[string]Gate{"nah": {BudgetPct: pct(1)}}},
	}}
	first := validatePlanes(c).Error()
	for i := 0; i < 20; i++ {
		if got := validatePlanes(c).Error(); got != first {
			t.Fatalf("run %d reported %q, first run reported %q", i, got, first)
		}
	}
	if !strings.Contains(first, "alpha") {
		t.Errorf("the ordering is not alphabetical by flow: %q", first)
	}
}

// --- end to end through Load ----------------------------------------------

func TestPerFlowGatesSurviveAnActualYAMLLoad(t *testing.T) {
	// Every test above builds a Config in Go. This one pins the YAML keys —
	// a struct tag typo makes each of them pass and the feature do nothing.
	dir := t.TempDir()
	path := filepath.Join(dir, "retrace.yaml")
	yaml := `app: shop
gates:
  pixel:
    budget_pct: 1.5
    checkpoints:
      cart: 8
flows:
  checkout:
    command: echo hi
    gates:
      pixel:
        budget_pct: 5
`
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	g := c.ResolveGates("checkout")["pixel"]
	if g.BudgetPct == nil || *g.BudgetPct != 5 {
		t.Errorf("budget_pct = %+v, want 5 — `flows.<name>.gates.pixel.budget_pct` did not decode", g.BudgetPct)
	}
	if got := g.Checkpoints["cart"]; got != 8 {
		t.Errorf("cart = %v, want 8 — `gates.pixel.checkpoints` did not decode", got)
	}
	if g := c.ResolveGates("other")["pixel"]; g.BudgetPct == nil || *g.BudgetPct != 1.5 {
		t.Errorf("an unlisted flow got %+v, want the global 1.5", g.BudgetPct)
	}
}

// --- canonical ------------------------------------------------------------

func TestCanonicalComparesBothDimensions(t *testing.T) {
	// One dimension equal is still a mismatch, and it is the case a
	// half-written comparison passes: a viewport someone changed from
	// 390x844 to 390x800 is exactly the drift this guard exists to catch.
	c := &Canonical{Width: 390, Height: 844}
	if !c.Matches(390, 844) {
		t.Error("the declared size did not match itself")
	}
	if c.Matches(390, 800) {
		t.Error("same width, different height matched")
	}
	if c.Matches(400, 844) {
		t.Error("same height, different width matched")
	}
}

func TestNoCanonicalBlockMatchesEverything(t *testing.T) {
	// The overwhelming majority of flows. There is no expectation to violate.
	var c *Canonical
	if !c.Matches(1206, 2622) {
		t.Error("a flow with no canonical block rejected a run")
	}
}
