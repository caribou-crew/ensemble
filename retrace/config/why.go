package config

import (
	"fmt"
	"sort"
	"strings"

	"github.com/caribou-crew/ensemble/retrace/rules"
)

// ValidateWhy reports every tolerance in this config that carries no `why:`.
//
// A tolerance is anything that stops a difference from being reported: a
// wire rule, a wire_ignore glob, a mask rect, an expected 4xx/5xx status.
// Each one is a decision someone made that a difference does not matter,
// and each one outlives the person who made it. Six months later a reader
// finds `- path: "**.total"` in the list and cannot tell whether it is
// protecting the build from a genuinely volatile field or hiding a real
// regression someone silenced on a Friday — so nobody dares delete it, and
// the list only ever grows. `why` is the note that makes the entry
// reviewable; RequireWhy is the ratchet that stops the list growing without
// one.
//
// It reports ALL of them, not the first. A project turning the ratchet on
// has a backlog, and a validator that surfaces one entry per run turns a
// single afternoon's cleanup into N runs of whack-a-mole. The order is
// deterministic (config order within a kind; sorted keys for the two maps),
// so the list is diffable between runs and a fix can be checked off top to
// bottom.
//
// Deviations are NOT checked here, and their absence is not an oversight:
// diff.LoadDeviations already requires a non-blank `reason` on every entry,
// unconditionally, for every project. The strictest tolerance in the
// product needs no opt-in, so there is nothing for this ratchet to add.
//
// Built-in wire rules are not checked either — they are not in
// Config.WireRules, they carry their own Why (builtins.go), and a project
// cannot edit them, so failing a build over one would be an error with no
// available fix.
func (c *Config) ValidateWhy() error {
	var missing []string

	for i, r := range c.WireRules {
		if blank(r.Why) {
			missing = append(missing, fmt.Sprintf("wire_rules[%d] (%s)", i, describeRule(r)))
		}
	}
	for i, e := range c.WireIgnore {
		if blank(e.Why) {
			// The bare-scalar form (`- "**.id"`) cannot carry a why at all,
			// so name the object form in the message: the fix is a shape
			// change, not just a missing key, and a reader who does not know
			// that will try to append `why:` to a string.
			missing = append(missing, fmt.Sprintf("wire_ignore[%d] (%q — use the object form: {path: %q, why: …})", i, e.Path, e.Path))
		}
	}
	for i, s := range c.ExpectedStatuses {
		if blank(s.Why) {
			missing = append(missing, fmt.Sprintf("expected_statuses[%d] (%d on %s)", i, s.Status, s.Path))
		}
	}
	missing = append(missing, missingMaskWhys("masks", c.Masks)...)
	for _, name := range sortedFlowNames(c.Flows) {
		missing = append(missing, missingMaskWhys(fmt.Sprintf("flows.%s.masks", name), c.Flows[name].Masks)...)
	}

	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("require_why is on and %d tolerance(s) have no `why:`. A tolerance nobody explained "+
		"is indistinguishable from one added to silence a regression:\n  %s",
		len(missing), strings.Join(missing, "\n  "))
}

// blank treats a whitespace-only why as absent. `why: " "` satisfies a
// presence check and explains nothing, and a ratchet that can be defeated
// with a space is decoration.
func blank(s string) bool { return strings.TrimSpace(s) == "" }

func missingMaskWhys(prefix string, masks map[string][]Rect) []string {
	var out []string
	for _, cp := range sortedMaskCheckpoints(masks) {
		for i, r := range masks[cp] {
			if blank(r.Why) {
				out = append(out, fmt.Sprintf("%s.%s[%d] (%gx%g at %g,%g)", prefix, cp, i, r.Width, r.Height, r.X, r.Y))
			}
		}
	}
	return out
}

func sortedMaskCheckpoints(m map[string][]Rect) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedFlowNames(m map[string]Flow) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// describeRule names a wire rule by what it MATCHES, because that is what a
// reader has to look at to write the why — the index alone sends them
// counting list entries. Headers and globs are sorted for the same reason
// rules.Normalize sorts them: map iteration order is random, and an error
// message that reshuffles between two runs of the same config reads as two
// different problems.
func describeRule(r rules.Raw) string {
	var parts []string
	if r.Method != "" {
		parts = append(parts, r.Method)
	}
	if r.Path != "" {
		parts = append(parts, r.Path)
	}
	if t := sortedTargets(r.Headers, r.Body); len(t) > 0 {
		parts = append(parts, strings.Join(t, ", "))
	}
	if len(parts) == 0 {
		// A Raw with no method, path, headers or body matches everything
		// and tolerates nothing; it is already useless, but it must still
		// be nameable or the error points at an index with no description.
		return "empty rule"
	}
	return strings.Join(parts, " ")
}

func sortedTargets(headers, body map[string]any) []string {
	out := make([]string, 0, len(headers)+len(body))
	for k := range headers {
		out = append(out, k)
	}
	for k := range body {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
