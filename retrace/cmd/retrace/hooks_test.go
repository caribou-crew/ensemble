package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caribou-crew/ensemble/retrace/config"
)

// touchCmd is a shell command that records that it ran, in order, by
// appending to a file. Ordering between hooks is load-bearing (a discovery
// step feeding the next one is the normal shape), so the tests assert the
// sequence, not just the set.
func touchCmd(log, name string) string {
	return "printf '" + name + "\\n' >> " + log
}

func readLog(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func TestRunHooksRunsEveryCommandInDeclaredOrder(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "order.log")
	var stderr bytes.Buffer

	err := runHooks(context.Background(), "preflight",
		[]string{touchCmd(log, "first"), touchCmd(log, "second"), touchCmd(log, "third")},
		dir, os.Environ(), &stderr)
	if err != nil {
		t.Fatalf("runHooks: %v", err)
	}

	got := readLog(t, log)
	want := []string{"first", "second", "third"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("hooks ran %v, want %v", got, want)
	}
}

// The first failure stops the sequence. A later hook running after an earlier
// one failed would operate on a stack whose preparation is known incomplete.
func TestRunHooksStopsAtFirstFailure(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "order.log")
	var stderr bytes.Buffer

	err := runHooks(context.Background(), "preflight",
		[]string{touchCmd(log, "ran"), "exit 7", touchCmd(log, "must-not-run")},
		dir, os.Environ(), &stderr)
	if err == nil {
		t.Fatal("a non-zero hook must be an error")
	}

	if got := readLog(t, log); len(got) != 1 || got[0] != "ran" {
		t.Errorf("commands after the failure still ran: %v", got)
	}
}

// "preflight failed" without the command text sends someone to read their
// whole config. The failing command is the diagnostic.
func TestHookErrorNamesTheFailingCommand(t *testing.T) {
	dir := t.TempDir()
	var stderr bytes.Buffer

	err := runHooks(context.Background(), "flow preflight",
		[]string{"true", "exit 3 # seed-assert"}, dir, os.Environ(), &stderr)
	if err == nil {
		t.Fatal("expected an error")
	}
	msg := err.Error()
	for _, want := range []string{"flow preflight", "seed-assert", "2 of 2"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not mention %q", msg, want)
		}
	}
	var he *hookError
	if !errors.As(err, &he) {
		t.Fatalf("error is not a *hookError: %T", err)
	}
}

// A blank command is a hook that enforces nothing while the config claims a
// precondition is enforced — the exact silent no-op this file exists to stop.
// `sh -c ""` exits 0, so nothing else would ever catch it.
func TestRunHooksRejectsABlankCommand(t *testing.T) {
	var stderr bytes.Buffer
	err := runHooks(context.Background(), "setup", []string{"true", "   "}, t.TempDir(), os.Environ(), &stderr)
	if err == nil {
		t.Fatal("a blank hook command must be rejected, not silently skipped")
	}
	if !strings.Contains(err.Error(), "blank") {
		t.Errorf("error %q does not explain that the command was blank", err)
	}
}

// Hooks print to stderr only. Under --json, stdout carries the manifest alone
// as the documented CI contract, so a hook that echoes would corrupt the
// document a pipeline is parsing. runHooks takes no stdout writer at all —
// this pins that it stays that way.
func TestRunHooksWritesCommandOutputToStderr(t *testing.T) {
	var stderr bytes.Buffer
	if err := runHooks(context.Background(), "preflight",
		[]string{"echo hello-from-hook"}, t.TempDir(), os.Environ(), &stderr); err != nil {
		t.Fatalf("runHooks: %v", err)
	}
	if !strings.Contains(stderr.String(), "hello-from-hook") {
		t.Errorf("hook stdout did not reach stderr: %q", stderr.String())
	}
}

// A generic seed script shared across flows needs to know which flow it is
// preparing. Threading that through a per-flow wrapper would defeat the point
// of a declarative hook.
func TestHookEnvCarriesAppAndFlow(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "env.txt")
	var stderr bytes.Buffer

	if err := runHooks(context.Background(), "preflight",
		[]string{"printf '%s/%s' \"$RETRACE_APP\" \"$RETRACE_FLOW\" > " + out},
		dir, hookEnv("storefront", "checkout"), &stderr); err != nil {
		t.Fatalf("runHooks: %v", err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(b) != "storefront/checkout" {
		t.Errorf("hook saw %q, want %q", b, "storefront/checkout")
	}
}

// Global preflight gates the per-flow hooks. Running a flow's seed assertion
// before knowing the stack answered at all produces a failure that blames the
// flow when nothing is running.
func TestRunPreconditionsOrdersGlobalThenFlowThenSetup(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "order.log")
	var stderr bytes.Buffer

	cfg := &config.Config{Preflight: []string{touchCmd(log, "global-preflight")}}
	fl := config.Flow{
		Preflight: []string{touchCmd(log, "flow-preflight")},
		Setup:     []string{touchCmd(log, "setup")},
	}
	if err := runPreconditions(context.Background(), cfg, fl, "app", "flow", dir, &stderr); err != nil {
		t.Fatalf("runPreconditions: %v", err)
	}

	got := strings.Join(readLog(t, log), ",")
	want := "global-preflight,flow-preflight,setup"
	if got != want {
		t.Errorf("order = %q, want %q", got, want)
	}
}

// A failing global preflight must stop before the flow's own hooks — its
// whole job is to answer "is the stack even up" first.
func TestRunPreconditionsFailingGlobalSkipsFlowHooks(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "order.log")
	var stderr bytes.Buffer

	cfg := &config.Config{Preflight: []string{"exit 1"}}
	fl := config.Flow{
		Preflight: []string{touchCmd(log, "flow-preflight")},
		Setup:     []string{touchCmd(log, "setup")},
	}
	if err := runPreconditions(context.Background(), cfg, fl, "app", "flow", dir, &stderr); err == nil {
		t.Fatal("a failing global preflight must stop the run")
	}
	if got := readLog(t, log); len(got) != 0 {
		t.Errorf("flow hooks ran after the global preflight failed: %v", got)
	}
}

// A flow with no entry in retrace.yaml has no hooks and must capture exactly
// as it did before this feature existed.
func TestRunPreconditionsNoHooksIsANoOp(t *testing.T) {
	var stderr bytes.Buffer
	if err := runPreconditions(context.Background(), &config.Config{}, config.Flow{}, "app", "flow", t.TempDir(), &stderr); err != nil {
		t.Fatalf("a flow with no hooks must run clean, got: %v", err)
	}
	if stderr.Len() != 0 {
		t.Errorf("no-hook flow printed to stderr: %q", stderr.String())
	}
}
