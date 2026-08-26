package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/caribou-crew/ensemble/retrace/config"
)

// Selecting and commanding flows.
//
// One `retrace run` can record many flows, because the alternative — one
// process per flow — costs a driving agent a full turn per flow, and turns are
// the scarce resource in the loop this tool exists to close. Three forms:
//
//	retrace run --flow checkout -- npm test    one flow, explicit command
//	retrace run --flows checkout,browse        those flows, each its own command
//	retrace run                                every configured flow
//
// The bare form is the one an agent should reach for: it needs no knowledge of
// which flows exist, so a flow added to retrace.yaml is picked up without the
// agent's prompt changing.

// selectFlows resolves which flows this invocation records, in the order they
// will run.
//
// Bare `run` sorts the configured flows by name rather than taking map order,
// which in Go is deliberately random. Two identical invocations that record
// the same flows in a different order would produce run directories whose
// timestamps interleave differently every time — an irreproducibility with no
// upside, in a tool whose entire job is comparing one run to another.
func selectFlows(flow, flows string, cfg *config.Config) (names []string, multi bool, err error) {
	flow, flows = strings.TrimSpace(flow), strings.TrimSpace(flows)
	switch {
	case flow != "" && flows != "":
		return nil, false, fmt.Errorf("--flow and --flows are alternatives: pass one flow name, or a comma-separated list, not both")

	case flow != "":
		return []string{flow}, false, nil

	case flows != "":
		seen := map[string]bool{}
		for _, f := range strings.Split(flows, ",") {
			f = strings.TrimSpace(f)
			if f == "" {
				// A stray comma is a typo, and silently dropping it would
				// record fewer flows than the user listed while reporting
				// success on all of them.
				return nil, false, fmt.Errorf("--flows contains an empty name: %q", flows)
			}
			if seen[f] {
				// Recording the same flow twice in one invocation writes two
				// run dirs whose second silently wins "latest". A repeated
				// name is a typo every time.
				return nil, false, fmt.Errorf("--flows names %q twice", f)
			}
			seen[f] = true
			names = append(names, f)
		}
		return names, true, nil

	default:
		if len(cfg.Flows) == 0 {
			return nil, false, fmt.Errorf("no flow given and retrace.yaml configures none — pass --flow NAME, or add a flows: entry")
		}
		for name := range cfg.Flows {
			names = append(names, name)
		}
		sort.Strings(names)
		return names, true, nil
	}
}

// resolveFlowCommand decides what to execute for one flow: the command after
// `--` when given, otherwise the flow's own `command:` from retrace.yaml run
// through a shell.
//
// `flows.<name>.command` was parsed and never read before this — a config key
// that accepted a value and did nothing with it, which is the same defect
// class as the preflight hooks that never ran. Reading it here is what makes
// the bare and --flows forms possible at all: without a per-flow command there
// is nothing for a multi-flow run to execute.
func resolveFlowCommand(fl config.Flow, name string, explicit []string) ([]string, error) {
	if len(explicit) > 0 {
		return explicit, nil
	}
	if strings.TrimSpace(fl.Command) == "" {
		return nil, fmt.Errorf("flow %q has no command: give one after `--`, or set flows.%s.command in retrace.yaml", name, name)
	}
	return []string{hookShell, "-c", fl.Command}, nil
}

// checkExplicitCommandUsage rejects `-- <cmd>` against more than one flow.
//
// Applying one command to several flows would record the same traffic under
// several flow names, and every one of those recordings would then be diffed
// as though it were that flow. A manifest is a claim about what a flow does;
// this is the cheapest place to refuse to fabricate one.
func checkExplicitCommandUsage(names []string, explicit []string) error {
	if len(explicit) > 0 && len(names) > 1 {
		return fmt.Errorf("a command after `--` records one flow, but %d were selected (%s): "+
			"give each flow its own flows.<name>.command, or select a single flow with --flow",
			len(names), strings.Join(names, ", "))
	}
	return nil
}

// unknownFlows returns the selected names that retrace.yaml does not declare.
//
// Only meaningful for an explicit selection: `--flow` has always been allowed
// to name a flow with no config entry (it just has no hooks or command), but
// a name in `--flows` that matches nothing is a typo that would otherwise fail
// later with a confusing "no command" error naming a flow the user believes
// they configured.
func unknownFlows(names []string, cfg *config.Config) []string {
	var unknown []string
	for _, n := range names {
		if _, ok := cfg.Flows[n]; !ok {
			unknown = append(unknown, n)
		}
	}
	return unknown
}
