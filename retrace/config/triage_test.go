package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// loadTriageYaml writes a retrace.yaml and loads it, returning the config or
// the load error. Local to this file: every case here is about what `triage:`
// does at LOAD time, which is the only place these rules can be rejected —
// diff consumes them long after the operator has stopped reading output.
func loadTriageYaml(t *testing.T, body string) (*Config, error) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "retrace.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return Load(p)
}

func TestTriageRulesParse(t *testing.T) {
	c, err := loadTriageYaml(t, `
app: web
triage:
  - name: seed-drift
    label: seeds
    why: our hop plane moves whenever the fixture seed is regenerated
    when: { hop: moved, wire: same }
`)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(c.Triage) != 1 {
		t.Fatalf("Triage = %+v, want one rule", c.Triage)
	}
	r := c.Triage[0]
	if r.Name != "seed-drift" || r.Label != "seeds" || r.Why == "" {
		t.Errorf("rule = %+v, want name/label/why all carried through", r)
	}
	if r.When.Hop != TriageMoved || r.When.Wire != TriageSame {
		t.Errorf("when = %+v, want hop moved and wire same", r.When)
	}
	// The three signals the rule did not name stay EMPTY, which is "any" —
	// not "same". A constraint the author did not write must not be invented
	// for them, or a rule narrows itself every time a signal is added.
	if r.When.Pixel != "" || r.When.Spec != "" || r.When.Capture != "" {
		t.Errorf("when = %+v, want the unnamed signals left unconstrained", r.When)
	}
}

// TestAnUnnamedTriageRuleGetsItsIndex keeps `triage.rule` in --json pointing
// at something the reader can find. A rule reported as "" is a label with no
// traceable source, which is the thing this field exists to prevent.
func TestAnUnnamedTriageRuleGetsItsIndex(t *testing.T) {
	c, err := loadTriageYaml(t, `
app: web
triage:
  - label: seeds
    when: { hop: moved }
  - label: ours
    when: { pixel: moved }
`)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for i, want := range []string{"triage[0]", "triage[1]"} {
		if c.Triage[i].Name != want {
			t.Errorf("Triage[%d].Name = %q, want %q", i, c.Triage[i].Name, want)
		}
	}
}

// TestARuleThatConstrainsNothingFailsLoad is the important one. An empty
// `when:` matches every run, and a config rule sits ABOVE the built-in table
// — so such a rule labels every diff in the project with one string, silently
// including the quarantined ones, and every rule below it is dead. It loads
// clean and runs clean; nothing but this check catches it.
func TestARuleThatConstrainsNothingFailsLoad(t *testing.T) {
	_, err := loadTriageYaml(t, `
app: web
triage:
  - label: ours
`)
	if err == nil {
		t.Fatal("Load accepted a triage rule with no constraints — it matches every run and shadows every rule below it, including the built-in harness row")
	}
	for _, want := range []string{"triage[0]", "ours", "every run"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestATriageRuleWithNoLabelFailsLoad(t *testing.T) {
	_, err := loadTriageYaml(t, `
app: web
triage:
  - when: { hop: moved }
`)
	if err == nil {
		t.Fatal("Load accepted a triage rule with no label — a rule that matches and reports nothing is not a classification")
	}
	if !strings.Contains(err.Error(), "triage[0]") {
		t.Errorf("error %q does not name the offending index", err)
	}
}

// TestAnUnknownTriageSignalValueFailsLoadNamingIt covers the typo that would
// otherwise turn into a rule that never matches: `moved` misspelled is not a
// weaker constraint, it is a dead rule, and a dead rule is invisible.
func TestAnUnknownTriageSignalValueFailsLoadNamingIt(t *testing.T) {
	_, err := loadTriageYaml(t, `
app: web
triage:
  - label: ours
    when: { hop: changed }
`)
	if err == nil {
		t.Fatal("Load accepted when.hop: changed — only \"moved\" and \"same\" are signal values")
	}
	for _, want := range []string{"hop", "changed", "moved", "same"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// TestAnUnknownTriageSignalNameFailsLoad relies on yaml.KnownFields, which is
// exactly why TriageWhen has named fields rather than a map[string]string: in
// a map, `pixle: moved` would be a key that silently never matches anything.
// The same failure validatePlanes exists to stop for `gates:`.
func TestAnUnknownTriageSignalNameFailsLoad(t *testing.T) {
	_, err := loadTriageYaml(t, `
app: web
triage:
  - label: ours
    when: { pixle: moved }
`)
	if err == nil {
		t.Fatal("Load accepted an unknown signal name in when: — a signal nobody knows is a rule that never fires, with no error to say so")
	}
	if !strings.Contains(err.Error(), "pixle") {
		t.Errorf("error %q does not name the offending key", err)
	}
}

// TestNoTriageKeyIsNotAnError: the built-in table is a total function over
// the five signals, so almost every project needs no `triage:` at all.
func TestNoTriageKeyIsNotAnError(t *testing.T) {
	c, err := loadTriageYaml(t, "app: web\n")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(c.Triage) != 0 {
		t.Errorf("Triage = %+v, want none", c.Triage)
	}
}

func TestTriageConstraintsAreReportedInPriorityOrder(t *testing.T) {
	w := TriageWhen{Pixel: TriageMoved, Capture: TriageSame, Hop: TriageMoved}
	got := w.Constraints()
	want := [][2]string{{"capture", TriageSame}, {"hop", TriageMoved}, {"pixel", TriageMoved}}
	if len(got) != len(want) {
		t.Fatalf("Constraints() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Constraints() = %v, want %v — the order decides how an error message reads, so it is fixed", got, want)
		}
	}
}
