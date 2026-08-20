package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/caribou-crew/ensemble/retrace/runs"
)

// --- helper processes -------------------------------------------------
//
// The test command `retrace run` executes is THIS test binary re-invoked
// with `-test.run <helper>` plus an env selector — the standard Go idiom,
// so the tests need no external tool (curl may be absent, and `go run`
// would collapse the child's exit code anyway).

func TestHelperFetchesThroughProxy(t *testing.T) {
	if os.Getenv("RETRACE_TEST_HELPER") != "fetch" {
		return
	}
	resp, err := http.Get(os.Getenv("RETRACE_PROXY_URL") + "/cart")
	if err != nil {
		fmt.Fprintln(os.Stderr, "helper fetch:", err)
		os.Exit(9)
	}
	resp.Body.Close()
	os.Exit(0)
}

func TestHelperExitsSeven(t *testing.T) {
	if os.Getenv("RETRACE_TEST_HELPER") != "exit7" {
		return
	}
	os.Exit(7)
}

func TestHelperPostsMarkers(t *testing.T) {
	if os.Getenv("RETRACE_TEST_HELPER") != "markers" {
		return
	}
	door := os.Getenv("RETRACE_MARKER_URL")
	post := func(path, body string) {
		resp, err := http.Post(door+path, "application/json", strings.NewReader(body))
		if err != nil {
			fmt.Fprintln(os.Stderr, "helper marker:", err)
			os.Exit(9)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			fmt.Fprintln(os.Stderr, "helper marker status:", resp.StatusCode)
			os.Exit(9)
		}
	}
	post("/group", `{"name":"login"}`)
	time.Sleep(5 * time.Millisecond)
	post("/group", `{"name":"checkout"}`)
	time.Sleep(5 * time.Millisecond)
	post("/group/end", `{}`)
	os.Exit(0)
}

// --- harness ----------------------------------------------------------

// buildRetrace builds the real binary. Exit codes are NEVER asserted
// through `go run` (global-constraints.md): it reports a non-zero child as
// its own exit 1, so every assertion that matters here — 2 for the config
// refusal, 3 for a usage error, 7 for the test command's own code — would
// read as 1.
func buildRetrace(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "retrace")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

type runResult struct {
	code           int
	stdout, stderr string
}

// runRetrace executes the built binary in cwd with helper selected by
// helper (may be ""), returning its real process exit code.
func runRetrace(t *testing.T, bin, cwd, helper string, args ...string) runResult {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "RETRACE_TEST_HELPER="+helper)
	var out, errOut strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	code := 0
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		code = exitErr.ExitCode()
	default:
		t.Fatalf("running retrace: %v", err)
	}
	return runResult{code: code, stdout: out.String(), stderr: errOut.String()}
}

// selfCmd is the `-- <test command>` tail: this test binary, filtered to
// one helper.
func selfCmd(t *testing.T, helper string) []string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	return []string{"--", self, "-test.run", "^" + helper + "$"}
}

func writeConfig(t *testing.T, cwd, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(cwd, "retrace.yaml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write retrace.yaml: %v", err)
	}
}

func onlyManifest(t *testing.T, cwd, app, flow string) runs.Manifest {
	t.Helper()
	root := runs.RunsRoot(cwd)
	ids := runs.ListRuns(root, app, flow)
	if len(ids) != 1 {
		t.Fatalf("run directories for %s/%s = %v, want exactly one", app, flow, ids)
	}
	p, err := runs.PathsFor(root, app, flow, ids[0])
	if err != nil {
		t.Fatalf("PathsFor: %v", err)
	}
	m, err := runs.ReadManifest(p.ManifestPath)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	return m
}

// --- tests ------------------------------------------------------------

func TestRunStandaloneRecordsAndWritesAManifest(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, "app: web\nredact:\n  - token\n")

	args := append([]string{"run", "--flow", "checkout", "--app", "web", "--upstream", upstream.URL},
		selfCmd(t, "TestHelperFetchesThroughProxy")...)
	res := runRetrace(t, bin, cwd, "fetch", args...)
	if res.code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", res.code, res.stdout, res.stderr)
	}

	m := onlyManifest(t, cwd, "web", "checkout")
	if m.Mode != runs.ModeStandalone {
		t.Errorf("mode = %q, want %q", m.Mode, runs.ModeStandalone)
	}
	if m.Test.ExitCode != 0 {
		t.Errorf("test.exitCode = %d, want 0", m.Test.ExitCode)
	}
	if m.Wire.Calls != 1 {
		t.Errorf("wire.calls = %d, want 1", m.Wire.Calls)
	}
	if m.Hops != nil {
		t.Errorf("hops = %+v, want absent in standalone mode", m.Hops)
	}
	if m.Flow != "checkout" || m.App != "web" {
		t.Errorf("app/flow = %q/%q", m.App, m.Flow)
	}
}

func TestRunRequiresFlow(t *testing.T) {
	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, "app: web\n")

	res := runRetrace(t, bin, cwd, "", "run", "--app", "web", "--upstream", "http://127.0.0.1:1", "--", "true")
	if res.code != exitUsage {
		t.Fatalf("exit = %d, want %d\nstderr: %s", res.code, exitUsage, res.stderr)
	}
	// Deliberately stricter than "mentions --flow": the usage banner names
	// every flag, so a substring match on "--flow" alone passes even when
	// `run` is not implemented at all.
	if !strings.Contains(res.stderr, "--flow is required") {
		t.Errorf("stderr does not say --flow is required: %s", res.stderr)
	}
}

// A failing test must fail the pipeline: retrace's own 0/1/2/3 contract
// only applies when the test command itself succeeded.
func TestRunPropagatesTheTestCommandsExitCode(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, "app: web\n")

	args := append([]string{"run", "--flow", "checkout", "--app", "web", "--upstream", upstream.URL},
		selfCmd(t, "TestHelperExitsSeven")...)
	res := runRetrace(t, bin, cwd, "exit7", args...)
	if res.code != 7 {
		t.Fatalf("exit = %d, want 7\nstdout: %s\nstderr: %s", res.code, res.stdout, res.stderr)
	}
	if m := onlyManifest(t, cwd, "web", "checkout"); m.Test.ExitCode != 7 {
		t.Errorf("test.exitCode = %d, want 7", m.Test.ExitCode)
	}
}

// This is the test that keeps flow-part groups from being a write-only
// feature: markers reach groups.jsonl and must be FOLDED into the
// manifest, or the wire diff's named sections silently collapse to one
// unnamed section on every real run while every unit test stays green.
func TestRunFoldsMarkersIntoManifestGroups(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, "app: web\n")

	args := append([]string{"run", "--flow", "checkout", "--app", "web", "--upstream", upstream.URL},
		selfCmd(t, "TestHelperPostsMarkers")...)
	res := runRetrace(t, bin, cwd, "markers", args...)
	if res.code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", res.code, res.stdout, res.stderr)
	}

	m := onlyManifest(t, cwd, "web", "checkout")
	if len(m.Groups) != 2 {
		t.Fatalf("groups = %+v, want 2", m.Groups)
	}
	if m.Groups[0].Name != "login" || m.Groups[1].Name != "checkout" {
		t.Fatalf("group names = %q, %q; want login, checkout in start order", m.Groups[0].Name, m.Groups[1].Name)
	}
	if !m.Groups[0].EndedAt.Equal(m.Groups[1].StartedAt) {
		t.Errorf("group %q ended at %v, want the next group's start %v",
			m.Groups[0].Name, m.Groups[0].EndedAt, m.Groups[1].StartedAt)
	}
}

// --- the config refusal (zero-value pin) ------------------------------

// config.Discover does not walk up the directory tree — that is a security
// property — so running from a subdirectory of a monorepo finds no config
// and capture would write UNREDACTED hops to disk. Config.Loaded's zero
// value (false) is the unsafe-to-proceed one on purpose, and `retrace run`
// refuses rather than degrading silently.
func TestRunRefusesToCaptureWhenNoConfigWasFound(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir() // deliberately no retrace.yaml

	args := append([]string{"run", "--flow", "checkout", "--app", "web", "--upstream", upstream.URL},
		selfCmd(t, "TestHelperFetchesThroughProxy")...)
	res := runRetrace(t, bin, cwd, "fetch", args...)
	if res.code != exitGate {
		t.Fatalf("exit = %d, want %d (hard gate)\nstdout: %s\nstderr: %s", res.code, exitGate, res.stdout, res.stderr)
	}
	want := filepath.Join(cwd, "retrace.yaml")
	if !strings.Contains(res.stderr, want) {
		t.Errorf("stderr does not name the absolute path %q:\n%s", want, res.stderr)
	}
	if !strings.Contains(res.stderr, "--no-config") {
		t.Errorf("stderr does not name the --no-config override:\n%s", res.stderr)
	}
	if entries, _ := os.ReadDir(runs.RunsRoot(cwd)); len(entries) != 0 {
		t.Errorf("a refused run wrote %d entries under the runs root", len(entries))
	}
}

// The refusal is keyed on Config.Loaded, NEVER on len(Redact): an empty
// redact list is correct and deliberate — baseline redaction lives in
// core/trace's redactor and config.Redact supplies user ADDITIONS on top.
// Mutating cmdRun to key the refusal on `len(cfg.Redact) == 0` makes this
// test fail (it would exit 2 instead of capturing).
func TestRunCapturesWithAConfigThatDeclaresNoRedactKeys(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, "app: web\n") // a real config, no redact: key at all

	args := append([]string{"run", "--flow", "checkout", "--app", "web", "--upstream", upstream.URL},
		selfCmd(t, "TestHelperFetchesThroughProxy")...)
	res := runRetrace(t, bin, cwd, "fetch", args...)
	if res.code != 0 {
		t.Fatalf("exit = %d, want 0 — an empty redact list is a valid config, not a missing one\nstderr: %s",
			res.code, res.stderr)
	}
	if m := onlyManifest(t, cwd, "web", "checkout"); m.Wire.Calls != 1 {
		t.Errorf("wire.calls = %d, want 1", m.Wire.Calls)
	}
}

func TestRunNoConfigOverridesTheRefusal(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir() // no retrace.yaml

	args := append([]string{"run", "--flow", "checkout", "--app", "web", "--no-config", "--upstream", upstream.URL},
		selfCmd(t, "TestHelperFetchesThroughProxy")...)
	res := runRetrace(t, bin, cwd, "fetch", args...)
	if res.code != 0 {
		t.Fatalf("exit = %d, want 0 with --no-config\nstdout: %s\nstderr: %s", res.code, res.stdout, res.stderr)
	}
	if m := onlyManifest(t, cwd, "web", "checkout"); m.Wire.Calls != 1 {
		t.Errorf("wire.calls = %d, want 1", m.Wire.Calls)
	}
}

// --json is the CI contract: the manifest, verbatim, on stdout.
func TestRunJSONEmitsTheManifestOnStdout(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, "app: web\n")

	args := append([]string{"run", "--flow", "checkout", "--app", "web", "--json", "--upstream", upstream.URL},
		selfCmd(t, "TestHelperFetchesThroughProxy")...)
	res := runRetrace(t, bin, cwd, "fetch", args...)
	if res.code != 0 {
		t.Fatalf("exit = %d, want 0\nstderr: %s", res.code, res.stderr)
	}
	// The helper's own `PASS`/`ok` chatter goes to the test command's
	// stdout, so find the JSON document rather than assuming it is alone.
	i := strings.Index(res.stdout, "{")
	if i < 0 {
		t.Fatalf("no JSON on stdout:\n%s", res.stdout)
	}
	var m runs.Manifest
	if err := json.Unmarshal([]byte(res.stdout[i:]), &m); err != nil {
		t.Fatalf("stdout is not a manifest: %v\n%s", err, res.stdout[i:])
	}
	if m.Schema != runs.Schema || m.Flow != "checkout" {
		t.Fatalf("manifest = %+v", m)
	}
}
