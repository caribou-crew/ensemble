package main

import (
	"strings"
	"testing"

	"github.com/caribou-crew/ensemble/retrace/config"
)

func cfgWithFlows(names ...string) *config.Config {
	c := &config.Config{Flows: map[string]config.Flow{}}
	for _, n := range names {
		c.Flows[n] = config.Flow{Command: "echo " + n}
	}
	return c
}

func TestSelectFlowsSingleFlagSelectsThatFlow(t *testing.T) {
	names, multi, err := selectFlows("checkout", "", cfgWithFlows("checkout", "browse"))
	if err != nil {
		t.Fatalf("selectFlows: %v", err)
	}
	if len(names) != 1 || names[0] != "checkout" {
		t.Errorf("names = %v, want [checkout]", names)
	}
	// --flow is the single-flow form, and --json's document shape follows the
	// invocation, so this must not report multi.
	if multi {
		t.Error("--flow must not select the multi-flow form")
	}
}

func TestSelectFlowsListPreservesGivenOrder(t *testing.T) {
	names, multi, err := selectFlows("", "browse, checkout", cfgWithFlows("checkout", "browse"))
	if err != nil {
		t.Fatalf("selectFlows: %v", err)
	}
	if strings.Join(names, ",") != "browse,checkout" {
		t.Errorf("names = %v, want [browse checkout] in the order given", names)
	}
	if !multi {
		t.Error("--flows must select the multi-flow form")
	}
}

// Bare `run` sorts by name. Go map order is deliberately random, so without
// this two identical invocations record the same flows in different orders —
// an irreproducibility with no upside in a tool built on comparing runs.
func TestSelectFlowsBareRunIsSortedAndStable(t *testing.T) {
	cfg := cfgWithFlows("zeta", "alpha", "mid")
	first, multi, err := selectFlows("", "", cfg)
	if err != nil {
		t.Fatalf("selectFlows: %v", err)
	}
	if !multi {
		t.Error("bare run must select the multi-flow form")
	}
	if strings.Join(first, ",") != "alpha,mid,zeta" {
		t.Errorf("names = %v, want sorted [alpha mid zeta]", first)
	}
	for i := 0; i < 20; i++ {
		again, _, err := selectFlows("", "", cfg)
		if err != nil {
			t.Fatalf("selectFlows: %v", err)
		}
		if strings.Join(again, ",") != strings.Join(first, ",") {
			t.Fatalf("bare run order is not stable: %v then %v", first, again)
		}
	}
}

func TestSelectFlowsRejectsBothFlags(t *testing.T) {
	if _, _, err := selectFlows("checkout", "browse", cfgWithFlows("checkout", "browse")); err == nil {
		t.Fatal("--flow with --flows must be rejected as ambiguous")
	}
}

// A stray comma silently dropped would record fewer flows than the user
// listed, while reporting success on every one it did record.
func TestSelectFlowsRejectsEmptyName(t *testing.T) {
	if _, _, err := selectFlows("", "checkout,,browse", cfgWithFlows("checkout", "browse")); err == nil {
		t.Fatal("an empty name in --flows must be rejected")
	}
}

// Two run dirs for one flow in a single invocation: the second silently wins
// "latest", so a repeat is always a typo.
func TestSelectFlowsRejectsDuplicate(t *testing.T) {
	if _, _, err := selectFlows("", "checkout,checkout", cfgWithFlows("checkout")); err == nil {
		t.Fatal("a repeated name in --flows must be rejected")
	}
}

func TestSelectFlowsBareRunWithNoConfiguredFlowsExplainsItself(t *testing.T) {
	_, _, err := selectFlows("", "", &config.Config{})
	if err == nil {
		t.Fatal("bare run with no configured flows must be an error")
	}
	if !strings.Contains(err.Error(), "--flow") {
		t.Errorf("error %q does not name the way out", err)
	}
}

// flows.<name>.command was parsed and never read before item 5 — a config key
// that accepted a value and did nothing. This is the assertion that it is now
// load-bearing.
func TestResolveFlowCommandUsesTheConfiguredCommand(t *testing.T) {
	got, err := resolveFlowCommand(config.Flow{Command: "npm run e2e:checkout"}, "checkout", nil)
	if err != nil {
		t.Fatalf("resolveFlowCommand: %v", err)
	}
	want := []string{hookShell, "-c", "npm run e2e:checkout"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("command = %v, want %v", got, want)
	}
}

func TestResolveFlowCommandExplicitOverridesConfig(t *testing.T) {
	explicit := []string{"npm", "test"}
	got, err := resolveFlowCommand(config.Flow{Command: "should-not-run"}, "checkout", explicit)
	if err != nil {
		t.Fatalf("resolveFlowCommand: %v", err)
	}
	if strings.Join(got, "|") != "npm|test" {
		t.Errorf("command = %v, want the explicit one", got)
	}
}

func TestResolveFlowCommandNoCommandNamesTheFlowAndBothFixes(t *testing.T) {
	_, err := resolveFlowCommand(config.Flow{}, "checkout", nil)
	if err == nil {
		t.Fatal("a flow with no command must be an error")
	}
	for _, want := range []string{"checkout", "--", "flows.checkout.command"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// One command applied to several flows would record identical traffic under
// several flow names, and each recording would then be diffed as though it
// were that flow — a manifest is a claim about what a flow does.
func TestCheckExplicitCommandUsageRejectsCommandWithManyFlows(t *testing.T) {
	err := checkExplicitCommandUsage([]string{"a", "b"}, []string{"npm", "test"})
	if err == nil {
		t.Fatal("a `--` command with multiple flows must be rejected")
	}
	if !strings.Contains(err.Error(), "--flow") {
		t.Errorf("error %q does not name the single-flow way out", err)
	}
}

func TestCheckExplicitCommandUsageAllowsCommandWithOneFlow(t *testing.T) {
	if err := checkExplicitCommandUsage([]string{"a"}, []string{"npm", "test"}); err != nil {
		t.Errorf("a `--` command with one flow is the original form and must stay legal: %v", err)
	}
}

func TestCheckExplicitCommandUsageAllowsManyFlowsWithNoCommand(t *testing.T) {
	if err := checkExplicitCommandUsage([]string{"a", "b"}, nil); err != nil {
		t.Errorf("multiple flows with no `--` command is the normal multi-flow form: %v", err)
	}
}

func TestUnknownFlowsReportsOnlyTheUnconfiguredOnes(t *testing.T) {
	got := unknownFlows([]string{"checkout", "typo", "browse"}, cfgWithFlows("checkout", "browse"))
	if len(got) != 1 || got[0] != "typo" {
		t.Errorf("unknown = %v, want [typo]", got)
	}
}
