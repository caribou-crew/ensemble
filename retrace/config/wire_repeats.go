package config

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// WireRepeatEntry allowlists a client repeating a recorded endpoint under
// `retrace replay --assert-requests` (retrace/cmd/retrace/cmd_replay.go):
// an extra call diff.DiffWire classifies "repeat" for this Method+Path is
// tolerated as long as the run's total repeat count for that endpoint
// stays within MaxExtra. A "new" extra (an endpoint the reference never
// recorded at all) is never covered by this list — see cmd_replay.go's
// applyWireRepeats.
type WireRepeatEntry struct {
	Method string `yaml:"method"`
	// Path is matched against the SAME NormalizedPath the classification
	// ran against (rules.MatchPathGlob's dialect), never the raw URL —
	// consistent with every other path-shaped tolerance in this file.
	Path     string   `yaml:"path"`
	MaxExtra MaxExtra `yaml:"max_extra"`
	// Why is required like every other tolerance under `require_why: true`
	// — an unexplained allowance is indistinguishable from one added to
	// silence a real new-endpoint regression. See ValidateWhy.
	Why string `yaml:"why"`
}

// MaxExtra is a wire_repeats budget: a non-negative call count, or the
// literal string "any" for an endpoint whose repeat count nobody wants to
// bound. It is a distinct type (rather than a plain int with a sentinel
// like -1) because a sentinel is a magic number the YAML author has to
// know exists; "any" reads as what it means.
type MaxExtra struct {
	Any bool
	N   int
}

// Allows reports whether n repeat calls fit this budget.
func (m MaxExtra) Allows(n int) bool {
	return m.Any || n <= m.N
}

func (m *MaxExtra) UnmarshalYAML(node *yaml.Node) error {
	if strings.TrimSpace(node.Value) == "any" && node.Kind == yaml.ScalarNode {
		m.Any = true
		return nil
	}
	var n int
	if err := node.Decode(&n); err != nil {
		return fmt.Errorf(`max_extra: %q is not a non-negative integer or "any"`, node.Value)
	}
	if n < 0 {
		return fmt.Errorf("max_extra: %d must be >= 0 (or \"any\")", n)
	}
	m.N = n
	return nil
}

// validateWireRepeats rejects an entry missing the fields it cannot be
// matched or explained without — mirroring FindDeviation's own required
// Method/Path (an empty one would match every verb, or every endpoint).
func validateWireRepeats(c *Config) error {
	for i, e := range c.WireRepeats {
		switch {
		case strings.TrimSpace(e.Method) == "":
			return fmt.Errorf("wire_repeats[%d]: method is required — an empty method would tolerate repeats of every verb on that path", i)
		case strings.TrimSpace(e.Path) == "":
			return fmt.Errorf("wire_repeats[%d]: path is required — an empty path glob matches every endpoint", i)
		}
	}
	return nil
}
