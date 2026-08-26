package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caribou-crew/ensemble/retrace/rules"
)

func writeYAML(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "retrace.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestValidateWhyPassesWhenEveryToleranceExplainsItself(t *testing.T) {
	c := &Config{
		WireRules:        []rules.Raw{{Headers: map[string]any{"traceparent": "ignore"}, Why: "unique per request by design"}},
		WireIgnore:       []WireIgnoreEntry{{Path: "**.id", Why: "assigned per run"}},
		ExpectedStatuses: []StatusRule{{Path: "/cart/999/checkout", Status: 404, Why: "user 999 does not exist; the 404 is the assertion"}},
		Masks:            map[string][]Rect{"catalog": {{X: 0, Y: 0, Width: 320, Height: 48, Why: "the clock"}}},
		Flows:            map[string]Flow{"checkout": {Masks: map[string][]Rect{"cart": {{Width: 10, Height: 10, Why: "avatar"}}}}},
	}
	if err := c.ValidateWhy(); err != nil {
		t.Errorf("ValidateWhy = %v, want nil", err)
	}
}

func TestValidateWhyCatchesEveryKindOfTolerance(t *testing.T) {
	// One un-explained entry of each kind, in one config. Separate tests per
	// kind would each pass while the validator skipped the other four —
	// this is the test that says the coverage is complete.
	c := &Config{
		WireRules:        []rules.Raw{{Headers: map[string]any{"x-total": "integer"}}},
		WireIgnore:       []WireIgnoreEntry{{Path: "**.id"}},
		ExpectedStatuses: []StatusRule{{Path: "/cart/999/checkout", Status: 404}},
		Masks:            map[string][]Rect{"catalog": {{Width: 320, Height: 48}}},
		Flows:            map[string]Flow{"checkout": {Masks: map[string][]Rect{"cart": {{Width: 10, Height: 10}}}}},
	}
	err := c.ValidateWhy()
	if err == nil {
		t.Fatal("ValidateWhy = nil, want an error naming all five")
	}
	for _, want := range []string{
		"wire_rules[0]", "wire_ignore[0]", "expected_statuses[0]",
		"masks.catalog[0]", "flows.checkout.masks.cart[0]",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not name %s:\n%v", want, err)
		}
	}
	if !strings.Contains(err.Error(), "5 tolerance(s)") {
		t.Errorf("error must total them, got:\n%v", err)
	}
}

func TestValidateWhyReportsEveryOffenderNotJustTheFirst(t *testing.T) {
	// A project turning the ratchet on has a backlog. A validator that
	// surfaced one entry per run would turn one afternoon's cleanup into N
	// runs of whack-a-mole, which is how a good check gets switched back off.
	c := &Config{WireIgnore: []WireIgnoreEntry{{Path: "**.a"}, {Path: "**.b"}, {Path: "**.c"}}}
	err := c.ValidateWhy()
	if err == nil {
		t.Fatal("want an error")
	}
	for _, want := range []string{"wire_ignore[0]", "wire_ignore[1]", "wire_ignore[2]"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not name %s:\n%v", want, err)
		}
	}
}

func TestValidateWhyNamesWhatARuleMatchesNotJustItsIndex(t *testing.T) {
	// The index alone sends a reader counting list entries. The description
	// is what lets them find the rule and decide what the why should say.
	c := &Config{WireRules: []rules.Raw{{
		Method: "POST", Path: "/cart/*",
		Body: map[string]any{"total": "integer", "applied": "ignore"},
	}}}
	err := c.ValidateWhy()
	if err == nil {
		t.Fatal("want an error")
	}
	// Sorted, because map iteration is random and an error message that
	// reshuffles between two runs of one config reads as two problems.
	if !strings.Contains(err.Error(), "POST /cart/* applied, total") {
		t.Errorf("error must describe what the rule matches, got:\n%v", err)
	}
}

func TestTheOffenderListIsOrderedTheSameWayOnEveryRun(t *testing.T) {
	// Masks and flows are MAPS, and Go randomizes map iteration. Without a
	// sort the same config produces a differently-ordered list on every run,
	// so a reader working top-to-bottom loses their place and a CI log
	// cannot be diffed against yesterday's. Two entries per map, because a
	// single-entry map has only one possible order and cannot detect this.
	c := &Config{
		Masks: map[string][]Rect{
			"zebra":   {{Width: 1, Height: 1}},
			"catalog": {{Width: 2, Height: 2}},
		},
		Flows: map[string]Flow{
			"zulu":     {Masks: map[string][]Rect{"cart": {{Width: 3, Height: 3}}}},
			"checkout": {Masks: map[string][]Rect{"cart": {{Width: 4, Height: 4}}}},
		},
	}
	want := []string{
		"masks.catalog[0]", "masks.zebra[0]",
		"flows.checkout.masks.cart[0]", "flows.zulu.masks.cart[0]",
	}
	for i := 0; i < 20; i++ {
		msg := c.ValidateWhy().Error()
		at := -1
		for _, w := range want {
			j := strings.Index(msg, w)
			if j < 0 {
				t.Fatalf("%s missing from:\n%s", w, msg)
			}
			if j < at {
				t.Fatalf("%s came out of order on run %d:\n%s", w, i, msg)
			}
			at = j
		}
	}
}

func TestARuleDescriptionOrdersItsTargetsTheSameWayOnEveryRun(t *testing.T) {
	// Same hazard, second map: Raw.Headers and Raw.Body. Repeated, because
	// with two keys a single unsorted run has a 50% chance of looking right.
	c := &Config{WireRules: []rules.Raw{{Body: map[string]any{"total": "integer", "applied": "ignore"}}}}
	for i := 0; i < 20; i++ {
		if msg := c.ValidateWhy().Error(); !strings.Contains(msg, "applied, total") {
			t.Fatalf("targets came out unsorted on run %d:\n%s", i, msg)
		}
	}
}

func TestAWhitespaceOnlyWhyIsNotAWhy(t *testing.T) {
	// A ratchet that can be defeated with a space bar is decoration.
	c := &Config{WireIgnore: []WireIgnoreEntry{{Path: "**.id", Why: "   \t "}}}
	if err := c.ValidateWhy(); err == nil {
		t.Error("ValidateWhy accepted a whitespace-only why")
	}
}

func TestTheWireIgnoreMessageTeachesTheObjectForm(t *testing.T) {
	// The bare-scalar form cannot carry a why at all, so "add why:" is not
	// an actionable instruction — the fix is a shape change.
	c := &Config{WireIgnore: []WireIgnoreEntry{{Path: "**.id"}}}
	err := c.ValidateWhy()
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "object form") || !strings.Contains(err.Error(), "path: \"**.id\"") {
		t.Errorf("error must show the object form, got:\n%v", err)
	}
}

func TestAnEmptyConfigHasNothingToExplain(t *testing.T) {
	// Zero tolerances is a pass, not a vacuous failure: a project with no
	// rules has hidden nothing.
	if err := (&Config{}).ValidateWhy(); err != nil {
		t.Errorf("ValidateWhy = %v, want nil", err)
	}
}

func TestRequireWhyOffLeavesUnexplainedTolerancesAlone(t *testing.T) {
	// Every config in the wild today is unexplained. The ratchet is opt-in
	// or it is a breaking change.
	dir := writeYAML(t, "app: web\nwire_ignore:\n  - \"**.id\"\n")
	if _, err := Discover(dir); err != nil {
		t.Errorf("Discover = %v, want nil — require_why is off by default", err)
	}
}

func TestRequireWhyOnFailsDiscover(t *testing.T) {
	dir := writeYAML(t, "app: web\nrequire_why: true\nwire_ignore:\n  - \"**.id\"\n")
	_, err := Discover(dir)
	if err == nil {
		t.Fatal("Discover = nil, want a config error")
	}
	if !strings.Contains(err.Error(), "wire_ignore[0]") {
		t.Errorf("error must name the entry, got: %v", err)
	}
}

func TestRequireWhyOnPassesWhenTheYAMLExplainsItself(t *testing.T) {
	dir := writeYAML(t, "app: web\nrequire_why: true\nwire_ignore:\n  - path: \"**.id\"\n    why: \"assigned per run\"\n")
	if _, err := Discover(dir); err != nil {
		t.Errorf("Discover = %v, want nil", err)
	}
}

func TestRequireWhyAlsoCoversTheMachineWrittenOverlay(t *testing.T) {
	// The ratchet checks AFTER the overlay merge on purpose. `retrace ref
	// rule` and POST /api/rule write real tolerances that nobody reviews at
	// authoring time; exempting them would aim the check at the half of the
	// list that already gets read in pull requests.
	dir := writeYAML(t, "app: web\nrequire_why: true\n")
	if err := AppendWireRule(dir, rules.Raw{Path: "/cart", Body: map[string]any{"total": "integer"}}); err != nil {
		t.Fatal(err)
	}
	_, err := Discover(dir)
	if err == nil {
		t.Fatal("Discover = nil, want the overlay rule to be caught")
	}
	if !strings.Contains(err.Error(), "/cart total") {
		t.Errorf("error must describe the overlay rule, got: %v", err)
	}

	// And a why on the overlay rule satisfies it — the fix has to exist, or
	// the check is a dead end for anyone using the review queue.
	dir2 := writeYAML(t, "app: web\nrequire_why: true\n")
	if err := AppendWireRule(dir2, rules.Raw{Path: "/cart", Body: map[string]any{"total": "integer"}, Why: "recomputed per cart"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Discover(dir2); err != nil {
		t.Errorf("Discover = %v, want nil once the overlay rule explains itself", err)
	}
}

func TestBuiltinWireRulesNeverTripTheRatchet(t *testing.T) {
	// They are not in Config.WireRules, they carry their own Why, and a
	// project cannot edit them — failing a build over one would be an error
	// with no available fix.
	dir := writeYAML(t, "app: web\nrequire_why: true\n")
	c, err := Discover(dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if err := c.ValidateWhy(); err != nil {
		t.Errorf("ValidateWhy = %v, want nil", err)
	}
	for _, r := range BuiltinWireRules() {
		if strings.TrimSpace(r.Why) == "" {
			t.Errorf("built-in %+v carries no why — a SUPPRESSED row nobody can explain", r.Headers)
		}
	}
}

func TestBuiltinHeaderWhyIsCaseInsensitiveAndEmptyForStrangers(t *testing.T) {
	// HeaderDiff.Name arrives however the server spelled it; every other
	// header lookup in this codebase folds case, and one that forgot would
	// silently drop the why off the row it exists to explain.
	if got := BuiltinHeaderWhy("Date"); got == "" {
		t.Error("BuiltinHeaderWhy(\"Date\") = \"\", want the date built-in's reason")
	}
	if got := BuiltinHeaderWhy("x-request-id"); got != "" {
		t.Errorf("BuiltinHeaderWhy(\"x-request-id\") = %q, want \"\"", got)
	}
}

func TestWhyRoundTripsThroughYAMLOnEveryToleranceThatTakesOne(t *testing.T) {
	// Each of these is a separate yaml tag on a separate struct. A missing
	// tag decodes to "" silently, which is exactly the state ValidateWhy
	// then reports as a missing why — a confusing failure to debug.
	dir := writeYAML(t, `app: web
wire_rules:
  - headers: { traceparent: ignore }
    why: "unique per request"
wire_ignore:
  - path: "**.id"
    why: "assigned per run"
expected_statuses:
  - path: "/cart/999/checkout"
    status: 404
    why: "user 999 does not exist"
masks:
  catalog:
    - { x: 0, y: 0, width: 320, height: 48, why: "the clock" }
`)
	c, err := Discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, got := range []struct{ what, val string }{
		{"wire_rules", c.WireRules[0].Why},
		{"wire_ignore", c.WireIgnore[0].Why},
		{"expected_statuses", c.ExpectedStatuses[0].Why},
		{"masks", c.Masks["catalog"][0].Why},
	} {
		if strings.TrimSpace(got.val) == "" {
			t.Errorf("%s: why did not round-trip through YAML", got.what)
		}
	}
}

func TestATypodWhyKeyIsAnErrorNotASilentEmptyWhy(t *testing.T) {
	// KnownFields(true) covers the structs yaml decodes directly; the point
	// here is that adding Why did not open a hole in it.
	dir := writeYAML(t, "app: web\nexpected_statuses:\n  - path: \"/x\"\n    status: 404\n    whyy: \"typo\"\n")
	if _, err := Discover(dir); err == nil {
		t.Error("a typo'd why key decoded silently — the setting appears to work and does nothing")
	}
}

// TestTheShippedSampleConfigSatisfiesItsOwnRatchet keeps sample/retrace.yaml
// honest. It is the file people copy from, it sets `require_why: true`, and
// it is not covered by any Go package's own tests — so without this it can
// rot into a sample that would fail on the first run for anyone who followed
// it. Reaching across the repo is deliberate and bounded to this one file.
func TestTheShippedSampleConfigSatisfiesItsOwnRatchet(t *testing.T) {
	const sample = "../../sample"
	if _, err := os.Stat(filepath.Join(sample, "retrace.yaml")); err != nil {
		t.Skipf("no sample config at %s: %v", sample, err)
	}
	c, err := Discover(sample)
	if err != nil {
		t.Fatalf("the shipped sample config does not load: %v", err)
	}
	if !c.RequireWhy {
		t.Error("the sample stopped setting require_why: true — it is where the practice is demonstrated")
	}
	if err := c.ValidateWhy(); err != nil {
		t.Errorf("the sample config has an unexplained tolerance: %v", err)
	}
}
