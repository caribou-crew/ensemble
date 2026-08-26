package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/caribou-crew/ensemble/retrace/runs"
)

// Multi-flow runs, driven through the real binary. The claim under test is
// that one process records many flows, each with its own command and its own
// run directory — the thing that saves a driving agent a turn per flow.

// selfFlowCommand is a `flows.<name>.command` string that re-invokes this test
// binary filtered to one helper — the config-file equivalent of selfCmd's
// `-- <cmd>` tail. Quoted because a temp path can contain spaces.
func selfFlowCommand(t *testing.T, helper string) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	return fmt.Sprintf("'%s' -test.run '^%s$'", self, helper)
}

// twoFlowConfig declares two flows that each fetch through the proxy.
func twoFlowConfig(t *testing.T) string {
	t.Helper()
	cmd := selfFlowCommand(t, "TestHelperFetchesThroughProxy")
	return fmt.Sprintf(`app: web
flows:
  browse:
    command: "%s"
  checkout:
    command: "%s"
`, cmd, cmd)
}

// Bare `run` records every configured flow. This is the form an agent should
// use: adding a flow to retrace.yaml changes what gets recorded without
// changing the command.
func TestRunBareRecordsEveryConfiguredFlow(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, twoFlowConfig(t))

	res := runRetrace(t, bin, cwd, "fetch", "run", "--app", "web", "--upstream", upstream.URL)
	if res.code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", res.code, res.stdout, res.stderr)
	}

	// One run directory per flow — not one run holding both, and not one flow
	// silently overwriting the other.
	for _, flow := range []string{"browse", "checkout"} {
		if m := onlyManifest(t, cwd, "web", flow); m.Wire.Calls != 1 {
			t.Errorf("flow %s: wire.calls = %d, want 1", flow, m.Wire.Calls)
		}
	}
}

// --flows records exactly the named subset, and nothing else configured.
func TestRunFlowsRecordsOnlyTheNamedSubset(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, twoFlowConfig(t))

	res := runRetrace(t, bin, cwd, "fetch", "run", "--app", "web", "--upstream", upstream.URL, "--flows", "checkout")
	if res.code != 0 {
		t.Fatalf("exit = %d, want 0\nstderr: %s", res.code, res.stderr)
	}

	if m := onlyManifest(t, cwd, "web", "checkout"); m.Wire.Calls != 1 {
		t.Errorf("checkout: wire.calls = %d, want 1", m.Wire.Calls)
	}
	if ids := runs.ListRuns(runs.RunsRoot(cwd), "web", "browse"); len(ids) != 0 {
		t.Errorf("browse was recorded despite not being selected: %v", ids)
	}
}

// The whole point of taking many flows is one invocation, one full picture. A
// failing flow must not stop the ones after it — that would hide whether they
// share the cause.
func TestRunContinuesAfterAFlowFailsAndReportsTheFailure(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	// "browse" sorts first and fails; "checkout" must still run. browse's
	// command is a bare shell exit rather than a helper process: the helper
	// selector is one env var on the retrace process, so two flows cannot
	// each pick a different helper — and a helper that no-ops exits 0, which
	// would make this test pass for the wrong reason.
	writeConfig(t, cwd, fmt.Sprintf(`app: web
flows:
  browse:
    command: "exit 7"
  checkout:
    command: "%s"
`, selfFlowCommand(t, "TestHelperFetchesThroughProxy")))

	res := runRetrace(t, bin, cwd, "fetch", "run", "--app", "web", "--upstream", upstream.URL)

	// The first non-zero code wins — a later success must not mask it.
	if res.code != 7 {
		t.Fatalf("exit = %d, want 7 (the failing flow's code)\nstderr: %s", res.code, res.stderr)
	}
	if ids := runs.ListRuns(runs.RunsRoot(cwd), "web", "checkout"); len(ids) != 1 {
		t.Errorf("checkout did not run after browse failed: %v runs", len(ids))
	}
}

// --json's document shape follows the INVOCATION, not the result count. If it
// followed the count, a consumer's parser would break the day a second flow
// was added to retrace.yaml.
func TestRunJSONShapeFollowsTheInvocationNotTheCount(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()

	bin := buildRetrace(t)

	t.Run("single flow is an object", func(t *testing.T) {
		cwd := t.TempDir()
		writeConfig(t, cwd, twoFlowConfig(t))
		res := runRetrace(t, bin, cwd, "fetch", "run", "--app", "web", "--upstream", upstream.URL, "--json", "--flow", "checkout")
		if res.code != 0 {
			t.Fatalf("exit = %d, want 0\nstderr: %s", res.code, res.stderr)
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(res.stdout), &obj); err != nil {
			t.Fatalf("--flow --json is not a single object: %v\nstdout: %s", err, res.stdout)
		}
	})

	// One flow selected by the MULTI form still yields an array. This is the
	// case that separates "shape follows invocation" from "shape follows
	// count" — with only a count rule, this would be an object.
	t.Run("one flow via --flows is still an array", func(t *testing.T) {
		cwd := t.TempDir()
		writeConfig(t, cwd, twoFlowConfig(t))
		res := runRetrace(t, bin, cwd, "fetch", "run", "--app", "web", "--upstream", upstream.URL, "--json", "--flows", "checkout")
		if res.code != 0 {
			t.Fatalf("exit = %d, want 0\nstderr: %s", res.code, res.stderr)
		}
		var arr []map[string]any
		if err := json.Unmarshal([]byte(res.stdout), &arr); err != nil {
			t.Fatalf("--flows --json is not an array: %v\nstdout: %s", err, res.stdout)
		}
		if len(arr) != 1 {
			t.Errorf("array holds %d manifests, want 1", len(arr))
		}
	})

	t.Run("bare run is an array of every flow", func(t *testing.T) {
		cwd := t.TempDir()
		writeConfig(t, cwd, twoFlowConfig(t))
		res := runRetrace(t, bin, cwd, "fetch", "run", "--app", "web", "--upstream", upstream.URL, "--json")
		if res.code != 0 {
			t.Fatalf("exit = %d, want 0\nstderr: %s", res.code, res.stderr)
		}
		var arr []map[string]any
		if err := json.Unmarshal([]byte(res.stdout), &arr); err != nil {
			t.Fatalf("bare run --json is not an array: %v\nstdout: %s", err, res.stdout)
		}
		if len(arr) != 2 {
			t.Errorf("array holds %d manifests, want 2", len(arr))
		}
	})
}

// A command after `--` records one flow. Applied to several it would record
// identical traffic under several names, and each would then be diffed as
// though it were that flow.
func TestRunRejectsAnExplicitCommandAgainstManyFlows(t *testing.T) {
	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, twoFlowConfig(t))

	res := runRetrace(t, bin, cwd, "", "run", "--app", "web", "--upstream", "http://127.0.0.1:1", "--", "true")
	if res.code != exitUsage {
		t.Fatalf("exit = %d, want %d\nstderr: %s", res.code, exitUsage, res.stderr)
	}
	if !strings.Contains(res.stderr, "--flow") {
		t.Errorf("stderr does not name the single-flow way out: %s", res.stderr)
	}
	if ids, _ := os.ReadDir(runs.RunsRoot(cwd)); len(ids) != 0 {
		t.Errorf("a rejected invocation still wrote %d run entries", len(ids))
	}
}

// A typo in --flows is caught before anything is recorded, not at that flow's
// turn — otherwise a five-flow run writes four recordings before reporting the
// fifth was never configured.
func TestRunRejectsUnknownFlowBeforeRecordingAnything(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, twoFlowConfig(t))

	res := runRetrace(t, bin, cwd, "fetch", "run", "--app", "web", "--upstream", upstream.URL, "--flows", "checkout,typo")
	if res.code != exitUsage {
		t.Fatalf("exit = %d, want %d\nstderr: %s", res.code, exitUsage, res.stderr)
	}
	if !strings.Contains(res.stderr, "typo") {
		t.Errorf("stderr does not name the unknown flow: %s", res.stderr)
	}
	// checkout is valid and sorts first, but nothing may have been recorded.
	if ids, _ := os.ReadDir(runs.RunsRoot(cwd)); len(ids) != 0 {
		t.Errorf("a rejected invocation recorded %d entries before failing", len(ids))
	}
}

// Global preflight is per invocation, not per flow: its scope is the stack.
// Re-running it per flow would charge a multi-flow run N times for one check,
// and a flaky one would fail a later flow for a condition true at the start.
func TestRunGlobalPreflightRunsOncePerInvocationNotPerFlow(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	cmd := selfFlowCommand(t, "TestHelperFetchesThroughProxy")
	writeConfig(t, cwd, fmt.Sprintf(`app: web
preflight:
  - "printf 'x' >> preflight.count"
flows:
  browse:
    command: "%s"
  checkout:
    command: "%s"
`, cmd, cmd))

	res := runRetrace(t, bin, cwd, "fetch", "run", "--app", "web", "--upstream", upstream.URL)
	if res.code != 0 {
		t.Fatalf("exit = %d, want 0\nstderr: %s", res.code, res.stderr)
	}

	b, err := os.ReadFile(cwd + "/preflight.count")
	if err != nil {
		t.Fatalf("global preflight never ran: %v", err)
	}
	if len(b) != 1 {
		t.Errorf("global preflight ran %d times across 2 flows, want 1", len(b))
	}
}

// Per-flow teardown runs at the end of ITS flow, not at the end of the whole
// invocation. Deferred in the loop body instead of a function, a two-flow run
// would hold both sessions open and run both teardowns at the very end.
func TestRunPerFlowTeardownRunsBetweenFlows(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	cmd := selfFlowCommand(t, "TestHelperFetchesThroughProxy")
	writeConfig(t, cwd, fmt.Sprintf(`app: web
flows:
  browse:
    command: "%s"
    setup:
      - "printf 'browse-setup\n' >> order.log"
    teardown:
      - "printf 'browse-teardown\n' >> order.log"
  checkout:
    command: "%s"
    setup:
      - "printf 'checkout-setup\n' >> order.log"
    teardown:
      - "printf 'checkout-teardown\n' >> order.log"
`, cmd, cmd))

	res := runRetrace(t, bin, cwd, "fetch", "run", "--app", "web", "--upstream", upstream.URL)
	if res.code != 0 {
		t.Fatalf("exit = %d, want 0\nstderr: %s", res.code, res.stderr)
	}

	got := strings.Join(hookLog2(t, cwd), ",")
	want := "browse-setup,browse-teardown,checkout-setup,checkout-teardown"
	if got != want {
		t.Errorf("hook order = %q, want %q — teardown must close its own flow, not the invocation", got, want)
	}
}

func hookLog2(t *testing.T, cwd string) []string {
	t.Helper()
	b, err := os.ReadFile(cwd + "/order.log")
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}
