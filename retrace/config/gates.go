package config

import (
	"fmt"
	"sort"
)

// ResolveGates returns the gates in force for one flow: the top-level
// `gates:` map with `flows.<name>.gates` layered on top.
//
// Always call this rather than reading Config.Gates directly. A consumer that
// read the global map alone would silently ignore every per-flow override; a
// consumer that read the flow's map alone would report a plane as ungated
// because that flow did not happen to mention it. Both failures are silent
// and both look like a passing build.
//
// An unknown flow name — or "" — resolves to the global gates unchanged,
// which is what a single-flow project and a hand-built Summary both need.
// It is not an error here: `retrace diff --flow x` against a config with no
// `flows:` block at all is an ordinary, supported thing to do.
//
// The returned map is always a fresh copy. Returning c.Gates itself when a
// flow has no overrides would hand callers the config's own map, and the one
// caller that mutated it would silently re-budget every other flow in the
// run — `retrace run` compares several flows in one process.
func (c *Config) ResolveGates(flow string) map[string]Gate {
	out := make(map[string]Gate, len(c.Gates))
	for plane, g := range c.Gates {
		out[plane] = copyGate(g)
	}
	for plane, over := range c.Flows[flow].Gates {
		out[plane] = mergeGate(out[plane], over)
	}
	return out
}

// mergeGate layers one flow's override for a plane over the global entry.
//
// The merge is per FIELD, not wholesale, and that is the whole design
// decision. Replacing the entry would mean a flow that widens its overall
// budget (`gates: {pixel: {budget_pct: 5}}`) silently DISCARDS the global
// per-checkpoint overrides — so loosening the flow would TIGHTEN the one
// screen someone had already declared noisy, from its 8% down to 5%. A knob
// should only ever change what it names.
func mergeGate(base, over Gate) Gate {
	out := copyGate(base)
	if over.BudgetPct != nil {
		pct := *over.BudgetPct
		out.BudgetPct = &pct
	}
	for name, pct := range over.Checkpoints {
		if out.Checkpoints == nil {
			out.Checkpoints = map[string]float64{}
		}
		out.Checkpoints[name] = pct
	}
	return out
}

// copyGate deep-copies a Gate so no returned value aliases the config. Both
// of a Gate's fields are references — a pointer and a map — so a shallow copy
// shares everything that matters.
func copyGate(g Gate) Gate {
	out := Gate{}
	if g.BudgetPct != nil {
		pct := *g.BudgetPct
		out.BudgetPct = &pct
	}
	if g.Checkpoints != nil {
		out.Checkpoints = make(map[string]float64, len(g.Checkpoints))
		for k, v := range g.Checkpoints {
			out.Checkpoints[k] = v
		}
	}
	return out
}

// BudgetFor reports the budget in force for one checkpoint on this gate, and
// whether it came from a per-checkpoint override rather than the plane's own
// budget_pct.
//
// The second return is not a convenience: it is what lets a report say WHICH
// budget a checkpoint was judged against. A run where one screen is allowed
// 8% and everything else 1.5% otherwise prints a single threshold that is
// true of neither.
//
// Reports ok=false when the plane has no budget at all AND this checkpoint
// has no override — the caller must not read a zero budget as "nothing may
// change", which is the strictest possible gate arrived at by accident.
func (g Gate) BudgetFor(checkpoint string) (budget float64, perCheckpoint, ok bool) {
	if pct, found := g.Checkpoints[checkpoint]; found {
		return pct, true, true
	}
	if g.BudgetPct == nil {
		return 0, false, false
	}
	return *g.BudgetPct, false, true
}

// validateGateCheckpoints rejects `checkpoints:` on a plane that has no
// checkpoints to key on.
//
// Left unchecked, `gates: {wire: {checkpoints: {cart: 8}}}` loads clean and
// does nothing at all — the user believes a budget is in force and there is
// no error anywhere to tell them otherwise. Same failure mode, and the same
// remedy, as validatePlanes' typo'd plane name.
func validateGateCheckpoints(where string, gates map[string]Gate) error {
	for _, plane := range sortedPlanes(gates) {
		if plane == "pixel" || len(gates[plane].Checkpoints) == 0 {
			continue
		}
		return fmt.Errorf("%s: %s: `checkpoints:` is only meaningful on the pixel plane — %s has no checkpoints to budget",
			where, plane, plane)
	}
	return nil
}

// sortedPlanes orders a gate map's keys so an error message names the same
// offender on every run. Go randomizes map iteration, so a config with two
// mistakes in it would otherwise report them alternately and a user fixing
// the one they were shown would see the error "move".
func sortedPlanes(gates map[string]Gate) []string {
	out := make([]string, 0, len(gates))
	for plane := range gates {
		out = append(out, plane)
	}
	sort.Strings(out)
	return out
}
