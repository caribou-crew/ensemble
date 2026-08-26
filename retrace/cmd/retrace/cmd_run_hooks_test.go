package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/caribou-crew/ensemble/retrace/runs"
)

// These drive the hooks through the real binary, because the behaviour being
// claimed is about `retrace run` as a whole: that a failed precondition costs
// no run directory, and that teardown survives every exit path. Neither is
// observable from runHooks alone.

// hookLog reads the file the config's hooks append to. Hooks run with Dir set
// to the working directory, so a relative path in the YAML lands here.
func hookLog(t *testing.T, cwd string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(cwd, "hooks.log"))
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// A stack that isn't in the state the flow needs produces a recording that
// diffs as though the app misbehaved. Refusing is the whole point of item 4:
// no run directory, exit 2, and the failing command named.
func TestRunRefusesToCaptureWhenPreflightFails(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, "app: web\npreflight:\n  - \"exit 4 # seed-assert-failed\"\n")

	args := append([]string{"run", "--flow", "checkout", "--app", "web", "--upstream", upstream.URL},
		selfCmd(t, "TestHelperFetchesThroughProxy")...)
	res := runRetrace(t, bin, cwd, "fetch", args...)

	if res.code != exitGate {
		t.Fatalf("exit = %d, want %d (hard gate)\nstderr: %s", res.code, exitGate, res.stderr)
	}
	if entries, _ := os.ReadDir(runs.RunsRoot(cwd)); len(entries) != 0 {
		t.Errorf("a refused capture wrote %d entries under the runs root", len(entries))
	}
	// Naming the command is the entire diagnostic: "preflight failed" alone
	// sends someone to read their whole config.
	if !strings.Contains(res.stderr, "seed-assert-failed") {
		t.Errorf("stderr does not name the failing command:\n%s", res.stderr)
	}
	if !strings.Contains(res.stderr, "refusing to capture") {
		t.Errorf("stderr does not say the capture was refused:\n%s", res.stderr)
	}
}

// The ordering claim, end to end: global preflight, then the flow's own
// preflight, then setup — all before the session — and teardown after it.
func TestRunRunsHooksAroundTheCaptureInOrder(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, `app: web
preflight:
  - "printf 'global-preflight\\n' >> hooks.log"
flows:
  checkout:
    preflight:
      - "printf 'flow-preflight\\n' >> hooks.log"
    setup:
      - "printf 'setup\\n' >> hooks.log"
    teardown:
      - "printf 'teardown\\n' >> hooks.log"
`)

	args := append([]string{"run", "--flow", "checkout", "--app", "web", "--upstream", upstream.URL},
		selfCmd(t, "TestHelperFetchesThroughProxy")...)
	res := runRetrace(t, bin, cwd, "fetch", args...)
	if res.code != 0 {
		t.Fatalf("exit = %d, want 0\nstderr: %s", res.code, res.stderr)
	}

	got := strings.Join(hookLog(t, cwd), ",")
	want := "global-preflight,flow-preflight,setup,teardown"
	if got != want {
		t.Errorf("hook order = %q, want %q", got, want)
	}
	// The capture still happened — hooks gate it, they don't replace it.
	if m := onlyManifest(t, cwd, "web", "checkout"); m.Wire.Calls != 1 {
		t.Errorf("wire.calls = %d, want 1", m.Wire.Calls)
	}
}

// Teardown must run when the flow FAILED. That is when leftover state matters
// most: the next run inherits it, and has no way to know this one left a mess.
func TestRunRunsTeardownEvenWhenTheFlowFails(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, `app: web
flows:
  checkout:
    teardown:
      - "printf 'teardown\\n' >> hooks.log"
`)

	args := append([]string{"run", "--flow", "checkout", "--app", "web", "--upstream", upstream.URL},
		selfCmd(t, "TestHelperExitsSeven")...)
	res := runRetrace(t, bin, cwd, "exit7", args...)

	// The test command's exit code still wins — teardown does not mask it.
	if res.code != 7 {
		t.Fatalf("exit = %d, want 7 (the flow's own code)\nstderr: %s", res.code, res.stderr)
	}
	if got := hookLog(t, cwd); len(got) != 1 || got[0] != "teardown" {
		t.Errorf("teardown did not run after a failing flow: %v\nstderr: %s", got, res.stderr)
	}
}

// Setup runs outside the recording window on purpose: a seed step captured as
// flow traffic would put the harness's own calls on the wire plane and diff
// them as though the app had made them.
func TestRunDoesNotRecordSetupTraffic(t *testing.T) {
	// The seed step has to make a REAL request for this test to mean
	// anything: if it silently no-ops, "one recorded call" is true for the
	// wrong reason and the assertion proves nothing. curl is the portable way
	// to issue it from a shell hook, so its absence is an honest skip rather
	// than a pass.
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not in PATH; skipping — a silently skipped seed would make this test vacuous")
	}

	var seedHits int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/seed") {
			atomic.AddInt64(&seedHits, 1)
		}
	}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	// The setup hook calls the upstream directly, exactly as a real seed
	// script would. No `|| true`: a seed that fails must fail the run.
	writeConfig(t, cwd, `app: web
flows:
  checkout:
    setup:
      - "curl -sf -o /dev/null `+upstream.URL+`/seed"
`)

	args := append([]string{"run", "--flow", "checkout", "--app", "web", "--upstream", upstream.URL},
		selfCmd(t, "TestHelperFetchesThroughProxy")...)
	res := runRetrace(t, bin, cwd, "fetch", args...)
	if res.code != 0 {
		t.Fatalf("exit = %d, want 0\nstderr: %s", res.code, res.stderr)
	}

	// Half one: the seed really happened. Without this the next assertion is
	// satisfied by a seed that never ran.
	if got := atomic.LoadInt64(&seedHits); got != 1 {
		t.Fatalf("setup hook made %d requests to /seed, want 1 — the seed did not run, so this test proves nothing", got)
	}
	// Half two: and it is not on the wire plane. Only the flow's own call is.
	if m := onlyManifest(t, cwd, "web", "checkout"); m.Wire.Calls != 1 {
		t.Errorf("wire.calls = %d, want 1 — setup traffic leaked onto the wire plane", m.Wire.Calls)
	}
}

// A flow with no hooks configured must behave exactly as it did before hooks
// existed, and say nothing about them.
func TestRunWithoutHooksIsUnchanged(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, "app: web\n")

	args := append([]string{"run", "--flow", "checkout", "--app", "web", "--upstream", upstream.URL},
		selfCmd(t, "TestHelperFetchesThroughProxy")...)
	res := runRetrace(t, bin, cwd, "fetch", args...)
	if res.code != 0 {
		t.Fatalf("exit = %d, want 0\nstderr: %s", res.code, res.stderr)
	}
	for _, noise := range []string{"preflight", "setup", "teardown"} {
		if strings.Contains(res.stderr, noise) {
			t.Errorf("a hookless run mentioned %q:\n%s", noise, res.stderr)
		}
	}
}
