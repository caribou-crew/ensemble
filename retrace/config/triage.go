package config

import "fmt"

// TriageSame and TriageMoved are the only two values a TriageWhen constraint
// may hold. The third state — the empty string — is "this rule does not care
// about that signal", which is why TriageWhen's fields are strings rather
// than bools: a bool can express moved/same but not unconstrained, and a
// *bool would make the common case (constrain two signals, ignore three) a
// wall of pointer literals in Go and indistinguishable from `null` in YAML.
const (
	TriageSame  = "same"
	TriageMoved = "moved"
)

// TriageWhen constrains a triage rule to a subset of the five moved/same
// signals. An EMPTY field means "any" — the rule matches whether that signal
// moved or not.
//
// Named fields rather than a map[string]string so yaml.KnownFields catches a
// typo: `pixle: moved` is a load error here, where in a map it would be a key
// that silently never matches. That is the same failure validatePlanes exists
// to stop for `gates:`.
type TriageWhen struct {
	Pixel   string `yaml:"pixel"`
	Wire    string `yaml:"wire"`
	Hop     string `yaml:"hop"`
	Spec    string `yaml:"spec"`
	Capture string `yaml:"capture"`
	// Stack is true when the two runs were recorded against demonstrably
	// different backends — a service fingerprint that moved, or a different
	// seed. Unlike the other five it is not a plane of the comparison at all;
	// it is evidence about the conditions the comparison was made under.
	Stack string `yaml:"stack"`
}

// Constraints returns the rule's non-empty constraints keyed by signal name,
// in a fixed order so callers that report them produce a stable string.
// Signal names match the field names lower-cased, which are also the YAML
// keys and the json keys of diff.TriageSignals — one vocabulary, three
// surfaces.
func (w TriageWhen) Constraints() [][2]string {
	var out [][2]string
	for _, c := range [][2]string{
		{"capture", w.Capture},
		{"stack", w.Stack},
		{"wire", w.Wire},
		{"hop", w.Hop},
		{"spec", w.Spec},
		{"pixel", w.Pixel},
	} {
		if c[1] != "" {
			out = append(out, c)
		}
	}
	return out
}

// TriageRule is one row of the triage table: a constraint over the five
// signals and the label to report when every named constraint holds. Rules
// are matched in order, first match wins.
//
// Rules from retrace.yaml are consulted BEFORE the built-in table, so a
// project can specialise a classification without restating the defaults it
// still wants. Prepending rather than replacing is deliberate: a config that
// replaced the table wholesale would most often lose the `harness` row, and
// a run whose capture was not trustworthy silently classified as a code
// problem is the single most expensive misread this field can produce.
type TriageRule struct {
	// Name identifies the rule in diff --json's `triage.rule`. Optional; the
	// loader fills an empty one with its index (`triage[0]`) so the reported
	// rule always points at something the reader can find in their config.
	Name  string     `yaml:"name"`
	Label string     `yaml:"label"`
	When  TriageWhen `yaml:"when"`
	// Why is the house rule applied to a classification override: a rule that
	// relabels a run needs a reason a reader can evaluate a year later. It is
	// not enforced — a triage rule is not a tolerance, it does not suppress a
	// finding, and turning a missing why into a load error here would be a
	// stricter rule than `masks:` and `wire_ignore:` themselves carry.
	Why string `yaml:"why"`
}

// validateTriage rejects a triage rule that cannot mean what its author
// intended: an unknown signal value, a missing label, or a rule that
// constrains nothing.
//
// The constrains-nothing case is the important one. An empty `when:` matches
// every run, so such a rule — sitting above the built-in table, as all config
// rules do — would label every diff in the project with one string, including
// the quarantined ones. That is a config which loads clean, runs clean, and
// makes the field useless. Caught at Load, naming the index.
func validateTriage(c *Config) error {
	for i := range c.Triage {
		r := &c.Triage[i]
		if r.Label == "" {
			return fmt.Errorf("triage[%d]: needs a label — a rule that matches but reports nothing is not a classification", i)
		}
		cs := r.When.Constraints()
		if len(cs) == 0 {
			return fmt.Errorf("triage[%d] (%s): `when:` constrains no signal, so this rule matches every run and every rule below it is dead — name at least one of pixel, wire, hop, spec, capture", i, r.Label)
		}
		for _, c := range cs {
			if c[1] != TriageSame && c[1] != TriageMoved {
				return fmt.Errorf("triage[%d] (%s): when.%s is %q, want %q or %q", i, r.Label, c[0], c[1], TriageMoved, TriageSame)
			}
		}
		if r.Name == "" {
			r.Name = fmt.Sprintf("triage[%d]", i)
		}
	}
	return nil
}
