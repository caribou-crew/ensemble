package main

// cmd_diff_test.go drives `retrace diff` through the real entry point — a
// BUILT binary (buildRetrace/runRetrace, shared with cmd_run_test.go), never
// `go run` (which collapses every non-zero child to 1 — see
// global-constraints.md) — because Task 10 is the composition point for the
// whole diff phase: every plane it wires together is already unit-tested in
// isolation, and the dominant defect class in this plan has been wiring
// mistakes that only show up when the real entry point runs end to end.
//
// Fixture runs are produced by `retrace run` itself (also through the built
// binary), not hand-built manifests, wherever a real recording can make the
// point — the same "drive the real path" discipline diff.Build's own tests
// apply one layer down.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/caribou-crew/ensemble/retrace/runs"
)

// TestHelperFetchesAndPostsMarkers is the `-- <test command>` tail for
// TestSectionsComeFromTheManifestsGroups: unlike TestHelperPostsMarkers
// (cmd_run_test.go), it makes a real request through RETRACE_PROXY_URL
// inside each group so the run has actual wire calls — capture.Assess
// grades a run with zero calls "degraded", and capture.Fatal treats
// degraded as fatal, so a marker-only helper can never produce the clean
// (VerdictOK) capture this test needs to isolate Sections/group behavior.
func TestHelperFetchesAndPostsMarkers(t *testing.T) {
	if os.Getenv("RETRACE_TEST_HELPER") != "fetch-markers" {
		return
	}
	proxy := os.Getenv("RETRACE_PROXY_URL")
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
	fetch := func(path string) {
		resp, err := http.Get(proxy + path)
		if err != nil {
			fmt.Fprintln(os.Stderr, "helper fetch:", err)
			os.Exit(9)
		}
		resp.Body.Close()
	}
	post("/group", `{"name":"login"}`)
	fetch("/login")
	time.Sleep(5 * time.Millisecond)
	post("/group", `{"name":"checkout"}`)
	fetch("/checkout")
	time.Sleep(5 * time.Millisecond)
	post("/group/end", `{}`)
	os.Exit(0)
}

// settlePastRunIDResolution waits out NewRunID's 1-second, timestamp-first
// resolution (see runs/paths.go) between two `retrace run` calls in the
// same test: two runs of the same app/flow inside the same wall-clock
// second collide on `mkdir` (runs.Create refuses to let two runs silently
// share one directory) rather than producing two comparable runs.
func settlePastRunIDResolution() { time.Sleep(1100 * time.Millisecond) }

// runOnce runs `retrace run` once against upstream and returns the new
// run's id (the newest one after the call, by NewRunID's timestamp-first
// lexical order).
func runOnce(t *testing.T, bin, cwd, app, flow, upstreamURL string) string {
	t.Helper()
	settlePastRunIDResolution()
	before := map[string]bool{}
	for _, id := range runs.ListRuns(runs.RunsRoot(cwd), app, flow) {
		before[id] = true
	}
	args := append([]string{"run", "--flow", flow, "--app", app, "--upstream", upstreamURL},
		selfCmd(t, "TestHelperFetchesThroughProxy")...)
	res := runRetrace(t, bin, cwd, "fetch", args...)
	if res.code != 0 {
		t.Fatalf("retrace run: exit = %d\nstdout: %s\nstderr: %s", res.code, res.stdout, res.stderr)
	}
	ids := runs.ListRuns(runs.RunsRoot(cwd), app, flow)
	for _, id := range ids {
		if !before[id] {
			return id
		}
	}
	t.Fatalf("retrace run produced no new run directory under %s/%s", app, flow)
	return ""
}

func TestDiffExitsZeroOnIdenticalRuns(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	// See TestDiffJsonIsParseableAndCarriesTheVerdict: the two fetches are
	// seconds apart, so the upstream's auto-set "Date" response header
	// genuinely differs between A and B. Tolerate it via http-date, same
	// as a real retrace.yaml would.
	writeConfig(t, cwd, "app: web\nwire_rules:\n  - headers:\n      date: http-date\n")

	aID := runOnce(t, bin, cwd, "web", "checkout", upstream.URL)
	bID := runOnce(t, bin, cwd, "web", "checkout", upstream.URL)

	res := runRetrace(t, bin, cwd, "", "diff", "--flow", "checkout", "--app", "web", "--a", aID, "--b", bID)
	if res.code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", res.code, res.stdout, res.stderr)
	}
	if !strings.Contains(res.stdout, "pass") {
		t.Errorf("stdout does not mention the pass verdict:\n%s", res.stdout)
	}
}

func TestDiffExitsOneWhenAFieldChanged(t *testing.T) {
	// Two separate servers, each with fixed behavior — not one server whose
	// handler closes over a variable the test mutates between runs. A
	// keep-alive server goroutine can still be live (blocked reading the
	// next request) after runOnce returns, so a shared var written by the
	// test goroutine right after is a real, race-detector-flagged data
	// race, not just a theoretical one.
	upstreamA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true}`))
	}))
	defer upstreamA.Close()
	upstreamB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":false}`))
	}))
	defer upstreamB.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, "app: web\n")

	aID := runOnce(t, bin, cwd, "web", "checkout", upstreamA.URL)
	bID := runOnce(t, bin, cwd, "web", "checkout", upstreamB.URL)

	res := runRetrace(t, bin, cwd, "", "diff", "--flow", "checkout", "--app", "web", "--a", aID, "--b", bID)
	if res.code != 1 {
		t.Fatalf("exit = %d, want 1\nstdout: %s\nstderr: %s", res.code, res.stdout, res.stderr)
	}
}

func TestDiffExitsTwoOnAnUnexpected500(t *testing.T) {
	// See TestDiffExitsOneWhenAFieldChanged: two fixed-behavior servers,
	// not one server whose handler closes over a var the test mutates —
	// that pattern is a real data race under -race, not just a smell.
	upstreamA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer upstreamA.Close()
	upstreamB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer upstreamB.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, "app: web\n")

	aID := runOnce(t, bin, cwd, "web", "checkout", upstreamA.URL)
	bID := runOnce(t, bin, cwd, "web", "checkout", upstreamB.URL)

	res := runRetrace(t, bin, cwd, "", "diff", "--flow", "checkout", "--app", "web", "--a", aID, "--b", bID)
	if res.code != 2 {
		t.Fatalf("exit = %d, want 2 (an unexpected 500 is a hard gate failure)\nstdout: %s\nstderr: %s", res.code, res.stdout, res.stderr)
	}
}

func TestDiffExitsThreeOnAQuarantinedSide(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, "app: web\n")

	// "true" never touches RETRACE_PROXY_URL and posts no markers, so
	// retrace never sees a single request of any kind — the same fixture
	// TestRunBannersANonOkVerdict uses to reach VerdictBroken.
	settlePastRunIDResolution()
	before := map[string]bool{}
	for _, id := range runs.ListRuns(runs.RunsRoot(cwd), "web", "checkout") {
		before[id] = true
	}
	res := runRetrace(t, bin, cwd, "", "run", "--flow", "checkout", "--app", "web", "--upstream", upstream.URL, "--", "true")
	if res.code != 0 {
		t.Fatalf("retrace run: exit = %d\nstdout: %s\nstderr: %s", res.code, res.stdout, res.stderr)
	}
	var brokenID string
	for _, id := range runs.ListRuns(runs.RunsRoot(cwd), "web", "checkout") {
		if !before[id] {
			brokenID = id
		}
	}
	if brokenID == "" {
		t.Fatal("no run directory produced by the broken recording")
	}

	cleanID := runOnce(t, bin, cwd, "web", "checkout", upstream.URL)

	res2 := runRetrace(t, bin, cwd, "", "diff", "--flow", "checkout", "--app", "web", "--a", brokenID, "--b", cleanID)
	if res2.code != 3 {
		t.Fatalf("exit = %d, want 3 (a broken side must be quarantined, not silently diffed)\nstdout: %s\nstderr: %s", res2.code, res2.stdout, res2.stderr)
	}
	if !strings.Contains(res2.stdout, "QUARANTINED") {
		t.Errorf("stdout does not mention the quarantine:\n%s", res2.stdout)
	}
}

func TestDiffJsonIsParseableAndCarriesTheVerdict(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	// The two fetches are seconds apart (settlePastRunIDResolution sleeps
	// past run-ID collision resolution between them), so the upstream's
	// auto-set "Date" response header genuinely differs between A and B.
	// That's not a real regression — it's every HTTP server's clock —
	// so the fixture config tells wire diffing to tolerate it via the
	// http-date matcher, same as a real retrace.yaml would.
	writeConfig(t, cwd, "app: web\nwire_rules:\n  - headers:\n      date: http-date\n")

	aID := runOnce(t, bin, cwd, "web", "checkout", upstream.URL)
	bID := runOnce(t, bin, cwd, "web", "checkout", upstream.URL)

	res := runRetrace(t, bin, cwd, "", "diff", "--flow", "checkout", "--app", "web", "--a", aID, "--b", bID, "--json")
	if res.code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", res.code, res.stdout, res.stderr)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(res.stdout), &doc); err != nil {
		t.Fatalf("--json stdout is not parseable JSON: %v\nstdout: %s", err, res.stdout)
	}
	if doc["verdict"] != "pass" {
		t.Fatalf("verdict = %v, want pass: %s", doc["verdict"], res.stdout)
	}
}

func TestDiffNamesTheMissingRunInsteadOfPanicking(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, "app: web\n")
	bID := runOnce(t, bin, cwd, "web", "checkout", upstream.URL)

	res := runRetrace(t, bin, cwd, "", "diff", "--flow", "checkout", "--app", "web", "--a", "does-not-exist", "--b", bID)
	if res.code != 3 {
		t.Fatalf("exit = %d, want 3 (usage/IO error, not a panic)\nstdout: %s\nstderr: %s", res.code, res.stdout, res.stderr)
	}
	if !strings.Contains(res.stderr, "does-not-exist") {
		t.Fatalf("stderr does not name the missing selector:\n%s", res.stderr)
	}
}

// TestSectionsComeFromTheManifestsGroups is the reading half of Task 4's
// TestRunFoldsMarkersIntoManifestGroups: two run dirs whose manifests carry
// groups ["login","checkout"], asserting the summary's Sections are named
// after them and every paired entry lands in the section its timestamp
// falls in.
func TestSectionsComeFromTheManifestsGroups(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	// See TestDiffJsonIsParseableAndCarriesTheVerdict: tolerate the
	// upstream's auto-set "Date" response header differing between runs
	// seconds apart, same as a real retrace.yaml would.
	writeConfig(t, cwd, "app: web\nwire_rules:\n  - headers:\n      date: http-date\n")

	runMarkers := func() string {
		settlePastRunIDResolution()
		before := map[string]bool{}
		for _, id := range runs.ListRuns(runs.RunsRoot(cwd), "web", "checkout") {
			before[id] = true
		}
		// TestHelperFetchesAndPostsMarkers (not TestHelperPostsMarkers,
		// which never calls through the proxy): a run with zero wire calls
		// rates capture.Assess "degraded", and capture.Fatal treats
		// degraded as fatal regardless of --allow-degraded (that flag only
		// disables the early quarantine return — see diff/summary.go's
		// Build). This fixture's job is to exercise Sections/group
		// plumbing, so it needs a genuinely clean capture, not a flag that
		// papers over a degraded one.
		args := append([]string{"run", "--flow", "checkout", "--app", "web", "--upstream", upstream.URL},
			selfCmd(t, "TestHelperFetchesAndPostsMarkers")...)
		res := runRetrace(t, bin, cwd, "fetch-markers", args...)
		if res.code != 0 {
			t.Fatalf("retrace run: exit = %d\nstdout: %s\nstderr: %s", res.code, res.stdout, res.stderr)
		}
		for _, id := range runs.ListRuns(runs.RunsRoot(cwd), "web", "checkout") {
			if !before[id] {
				return id
			}
		}
		t.Fatal("no new run directory")
		return ""
	}

	aID := runMarkers()
	bID := runMarkers()

	res := runRetrace(t, bin, cwd, "", "diff", "--flow", "checkout", "--app", "web", "--a", aID, "--b", bID, "--json")
	if res.code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", res.code, res.stdout, res.stderr)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(res.stdout), &doc); err != nil {
		t.Fatalf("--json stdout is not parseable JSON: %v\nstdout: %s", err, res.stdout)
	}
	sections, ok := doc["sections"].([]any)
	if !ok || len(sections) != 2 {
		t.Fatalf("sections = %v, want 2 (login, checkout): %s", doc["sections"], res.stdout)
	}
	names := []string{}
	for _, raw := range sections {
		sec, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("section is not an object: %v", raw)
		}
		names = append(names, sec["name"].(string))
	}
	if names[0] != "login" || names[1] != "checkout" {
		t.Fatalf("section names = %v, want [login checkout]", names)
	}
}

// TestHelperKillsSelfWithSIGKILL is the `-- <test command>` tail for
// TestDiffExitsThreeOnASignalKilledChild. `retrace run` is the only
// subcommand that execs a child (the `-- <test command>` tail); `retrace
// diff` never spawns one. So the way to reach a signal-killed *exec.Command
// from a black-box CLI test is to make the child kill ITSELF — no external
// process control needed, and no flakiness from racing a kill signal
// against the child's own startup.
func TestHelperKillsSelfWithSIGKILL(t *testing.T) {
	if os.Getenv("RETRACE_TEST_HELPER") != "self-sigkill" {
		return
	}
	_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
	// Unreachable if the signal took effect; exit non-zero-but-defined so a
	// platform where it didn't still fails loudly instead of looking like
	// a clean pass.
	os.Exit(1)
}

// TestDiffExitsThreeOnASignalKilledChild pins Task 10's ownership of the
// exit codes it does not itself produce (see the brief's "this task owns
// the exit contract" ruling, and cmd_run.go's ExitCode < 0 branch): a
// `retrace run` test command killed by a signal must not leak exec.
// ExitError.ExitCode()'s raw -1 (which the OS truncates to 255, outside the
// 0/1/2/3 contract) as retrace's own process exit code. It is a "could not
// evaluate" outcome, mapped to 3 alongside config/IO failures — asserted
// through the BUILT binary (never `go run`, which collapses every non-zero
// child to 1) and against the literal number, not the exitUsage constant,
// per global-constraints.md.
func TestDiffExitsThreeOnASignalKilledChild(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, "app: web\n")

	args := append([]string{"run", "--flow", "checkout", "--app", "web", "--upstream", upstream.URL},
		selfCmd(t, "TestHelperKillsSelfWithSIGKILL")...)
	res := runRetrace(t, bin, cwd, "self-sigkill", args...)
	if res.code != 3 {
		t.Fatalf("exit = %d, want 3 (a signal-killed child is \"could not evaluate\", not a diff result)\nstdout: %s\nstderr: %s", res.code, res.stdout, res.stderr)
	}
}
