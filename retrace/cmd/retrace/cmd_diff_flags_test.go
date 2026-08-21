package main

// cmd_diff_flags_test.go exercises the `retrace diff` FLAGS through the
// built binary. The Task 10 review found three of them — --no-fail,
// --allow-degraded and --images — inert as far as the suite was concerned:
// each could be made a no-op, or hardcoded to its zero value, with a green
// run. Every internal was unit-tested and the wiring was not, which is the
// Task-8 defect class the brief named and the global constraint on tests
// whose input production can never construct.
//
// Exit codes are asserted through the BUILT BINARY and pinned as LITERAL
// numbers, never named constants and never `go run` (which reports a
// non-zero child as its own exit 1, collapsing 2 and 3 into the one value
// that happens to pass).

import (
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caribou-crew/ensemble/retrace/runs"
)

// TestHelperFetchesAndShoots is a `-- <test command>` tail that both calls
// through the proxy AND writes a checkpoint PNG into the run's shots/
// directory — the production path an adapter uses (capture.Session's
// Checkpoints() reads RETRACE_RUN_DIR/shots/*.png and its geometry from
// each PNG header). "shot-a" paints a flat square; "shot-b" paints the same
// square with a red rectangle in it, so the two runs' checkpoint genuinely
// differs and Build has something to draw a diff image of.
func TestHelperFetchesAndShoots(t *testing.T) {
	mode := os.Getenv("RETRACE_TEST_HELPER")
	if mode != "shot-a" && mode != "shot-b" {
		return
	}
	if proxy := os.Getenv("RETRACE_PROXY_URL"); proxy != "" {
		resp, err := http.Get(proxy + "/cart")
		if err != nil {
			fmt.Fprintln(os.Stderr, "helper fetch:", err)
			os.Exit(9)
		}
		resp.Body.Close()
	}
	runDir := os.Getenv("RETRACE_RUN_DIR")
	if runDir == "" {
		fmt.Fprintln(os.Stderr, "helper: RETRACE_RUN_DIR is unset")
		os.Exit(9)
	}
	img := image.NewRGBA(image.Rect(0, 0, 40, 40))
	base := color.RGBA{R: 10, G: 20, B: 30, A: 255}
	for y := range 40 {
		for x := range 40 {
			img.SetRGBA(x, y, base)
		}
	}
	if mode == "shot-b" {
		for y := 5; y < 25; y++ {
			for x := 5; x < 25; x++ {
				img.SetRGBA(x, y, color.RGBA{R: 250, A: 255})
			}
		}
	}
	shots := filepath.Join(runDir, "shots")
	if err := os.MkdirAll(shots, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "helper mkdir:", err)
		os.Exit(9)
	}
	f, err := os.Create(filepath.Join(shots, "cart.png"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "helper create:", err)
		os.Exit(9)
	}
	if err := png.Encode(f, img); err != nil {
		fmt.Fprintln(os.Stderr, "helper encode:", err)
		os.Exit(9)
	}
	f.Close()
	os.Exit(0)
}

// runShot runs `retrace run` once with TestHelperFetchesAndShoots in the
// given mode and returns the new run's id.
func runShot(t *testing.T, bin, cwd, app, flow, upstreamURL, mode string) string {
	t.Helper()
	settlePastRunIDResolution()
	before := map[string]bool{}
	for _, id := range runs.ListRuns(runs.RunsRoot(cwd), app, flow) {
		before[id] = true
	}
	args := append([]string{"run", "--flow", flow, "--app", app, "--upstream", upstreamURL},
		selfCmd(t, "TestHelperFetchesAndShoots")...)
	res := runRetrace(t, bin, cwd, mode, args...)
	if res.code != 0 {
		t.Fatalf("retrace run: exit = %d\nstdout: %s\nstderr: %s", res.code, res.stdout, res.stderr)
	}
	for _, id := range runs.ListRuns(runs.RunsRoot(cwd), app, flow) {
		if !before[id] {
			return id
		}
	}
	t.Fatalf("retrace run produced no new run directory under %s/%s", app, flow)
	return ""
}

// --- --no-fail -------------------------------------------------------------

// TestNoFailForcesExitZeroButStillReportsGates is the Step-1 test the brief
// named and the one test in Task 10's list that was never written. Deleting
// the `if *noFail { code = exitOK }` override changed no test result.
//
// The "but still reports" half matters as much as the exit code: --no-fail
// is for a reporting run that must not break the build, and a reporting run
// that also blinds the reader has no reason to exist.
func TestNoFailForcesExitZeroButStillReportsGates(t *testing.T) {
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
	// gates.wire so a BUDGET: line exists to still be reported.
	writeConfig(t, cwd, "app: web\ngates:\n  wire:\n    budget_pct: 2\n")

	aID := runOnce(t, bin, cwd, "web", "checkout", upstreamA.URL)
	bID := runOnce(t, bin, cwd, "web", "checkout", upstreamB.URL)

	base := []string{"diff", "--flow", "checkout", "--app", "web", "--a", aID, "--b", bID}
	plain := runRetrace(t, bin, cwd, "", base...)
	if plain.code != 2 {
		t.Fatalf("without --no-fail: exit = %d, want 2\nstdout: %s\nstderr: %s", plain.code, plain.stdout, plain.stderr)
	}

	res := runRetrace(t, bin, cwd, "", append(base, "--no-fail")...)
	if res.code != 0 {
		t.Fatalf("with --no-fail: exit = %d, want 0\nstdout: %s\nstderr: %s", res.code, res.stdout, res.stderr)
	}
	if !strings.Contains(res.stdout, "GATE:") {
		t.Errorf("--no-fail suppressed the GATE: lines; a reporting run must still report what it found:\n%s", res.stdout)
	}
	if !strings.Contains(res.stdout, "BUDGET: wire") {
		t.Errorf("--no-fail suppressed the BUDGET: lines:\n%s", res.stdout)
	}
	if !strings.Contains(res.stdout, "VERDICT: failed") {
		t.Errorf("--no-fail changed the Verdict itself; it must override only the process exit code:\n%s", res.stdout)
	}
}

// TestNoFailDoesNotZeroAQuarantinedRun pins the team lead's ruling:
// --no-fail suppresses FINDINGS, not INABILITY TO RUN. A quarantined
// verdict still exits 3, alongside config and I/O failure — otherwise a
// report-only CI job reports success for a run that was never compared,
// which is the zero-value trap wearing a command-line flag. A config error
// already exits 3 regardless of the flag, so zeroing quarantine was also
// internally inconsistent.
func TestNoFailDoesNotZeroAQuarantinedRun(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, "app: web\n")

	brokenID := runBrokenRecording(t, bin, cwd, upstream.URL)
	cleanID := runOnce(t, bin, cwd, "web", "checkout", upstream.URL)

	res := runRetrace(t, bin, cwd, "", "diff", "--flow", "checkout", "--app", "web",
		"--a", brokenID, "--b", cleanID, "--no-fail")
	if res.code != 3 {
		t.Fatalf("exit = %d, want 3 — --no-fail must not turn \"we refused to look\" into success\nstdout: %s\nstderr: %s", res.code, res.stdout, res.stderr)
	}
	if !strings.Contains(res.stdout, "QUARANTINED") {
		t.Errorf("stdout does not mention the quarantine:\n%s", res.stdout)
	}
}

// TestNoFailDoesNotZeroAConfigError is the same ruling's other arm: an
// unreadable config exits 3 with or without the flag. It returns through
// fail() before the noFail check ever runs, and that is deliberate.
func TestNoFailDoesNotZeroAConfigError(t *testing.T) {
	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, "app: web\nnot_a_real_key: 1\n")

	res := runRetrace(t, bin, cwd, "", "diff", "--flow", "checkout", "--app", "web", "--no-fail")
	if res.code != 3 {
		t.Fatalf("exit = %d, want 3 (an unreadable config is inability to run, not a finding)\nstdout: %s\nstderr: %s", res.code, res.stdout, res.stderr)
	}
}

// TestNoFailStillZeroesAChangedRun is the arm that keeps the flag's own
// reason to exist pinned: a test asserting only "quarantine survives
// --no-fail" would pass just as happily against a --no-fail that did
// nothing at all.
func TestNoFailStillZeroesAChangedRun(t *testing.T) {
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

	base := []string{"diff", "--flow", "checkout", "--app", "web", "--a", aID, "--b", bID}
	if plain := runRetrace(t, bin, cwd, "", base...); plain.code != 1 {
		t.Fatalf("without --no-fail: exit = %d, want 1", plain.code)
	}
	res := runRetrace(t, bin, cwd, "", append(base, "--no-fail")...)
	if res.code != 0 {
		t.Fatalf("with --no-fail: exit = %d, want 0 (a \"changed\" verdict is a finding, and findings are exactly what the flag suppresses)\nstdout: %s", res.code, res.stdout)
	}
}

// --- --allow-degraded ------------------------------------------------------

// TestAllowDegradedComparesADegradedSideInsteadOfQuarantiningIt: hardcoding
// AllowDegraded to false at the BuildInput seam survived the whole suite,
// because no CLI test ever passed the flag. It is the flag that matters
// most: in production, incompleteCheck is only load-bearing UNDER
// --allow-degraded, because capture.Assess's own verdict makes
// quarantineCheck catch every other case first — so the one flag that makes
// that code reachable was the one flag nothing exercised.
func TestAllowDegradedComparesADegradedSideInsteadOfQuarantiningIt(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, "app: web\n")

	brokenID := runBrokenRecording(t, bin, cwd, upstream.URL)
	cleanID := runOnce(t, bin, cwd, "web", "checkout", upstream.URL)

	base := []string{"diff", "--flow", "checkout", "--app", "web", "--a", brokenID, "--b", cleanID}
	plain := runRetrace(t, bin, cwd, "", base...)
	if plain.code != 3 {
		t.Fatalf("without --allow-degraded: exit = %d, want 3 (quarantined)\nstdout: %s", plain.code, plain.stdout)
	}

	res := runRetrace(t, bin, cwd, "", append(base, "--allow-degraded")...)
	if res.code != 2 {
		t.Fatalf("with --allow-degraded: exit = %d, want 2 — the comparison must actually run, and a fatal capture then fails the build through gatesOf rather than refusing up front\nstdout: %s\nstderr: %s", res.code, res.stdout, res.stderr)
	}
	if strings.Contains(res.stdout, "QUARANTINED") {
		t.Errorf("--allow-degraded must skip the quarantine, not relabel it:\n%s", res.stdout)
	}
	if !strings.Contains(res.stdout, "capture") {
		t.Errorf("the degraded capture must still be reported once compared:\n%s", res.stdout)
	}
}

// --- --images / --out ------------------------------------------------------

// TestCheckpointImagesLandUnderSideBsRunDirectory pins the on-disk layout
// contract Tasks 12, 13 and 16 all read, which the review found asserted by
// a source comment only: writeCheckpointImages and writePNG were both at
// 0.0% coverage, and --out's default was flippable from b.Dir to a.Dir with
// a green suite.
//
// `shots/` is the SECOND path component, not the first: Task 13's
// safeShotPath joins (dir, "shots", base+".png") and rejects any name
// carrying a separator, so every side it serves must be a directory with a
// shots/ child — exactly what a run directory already is.
func TestCheckpointImagesLandUnderSideBsRunDirectory(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, "app: web\nwire_rules:\n  - headers:\n      date: http-date\n")

	aID := runShot(t, bin, cwd, "web", "checkout", upstream.URL, "shot-a")
	bID := runShot(t, bin, cwd, "web", "checkout", upstream.URL, "shot-b")
	aDir, bDir := runDirOf(t, cwd, "web", "checkout", aID), runDirOf(t, cwd, "web", "checkout", bID)

	// --images defaults to true, so this is the default invocation.
	res := runRetrace(t, bin, cwd, "", "diff", "--flow", "checkout", "--app", "web", "--a", aID, "--b", bID, "--json")
	if res.code != 1 {
		t.Fatalf("exit = %d, want 1 (the checkpoint genuinely changed)\nstdout: %s\nstderr: %s", res.code, res.stdout, res.stderr)
	}

	for _, rel := range []string{
		filepath.Join("diff", "shots", "cart.png"),
		filepath.Join("overlay", "shots", "cart.png"),
	} {
		if _, err := os.Stat(filepath.Join(bDir, rel)); err != nil {
			t.Errorf("%s is not under side B's run directory: %v", rel, err)
		}
		if _, err := os.Stat(filepath.Join(aDir, rel)); err == nil {
			t.Errorf("%s was written under side A's run directory — --out defaults to side B, the candidate", rel)
		}
	}

	var doc struct {
		Checkpoints []struct {
			Name   string `json:"name"`
			Images struct {
				A, B, Diff, Overlay string
			} `json:"images"`
		} `json:"checkpoints"`
	}
	if err := json.Unmarshal([]byte(res.stdout), &doc); err != nil {
		t.Fatalf("--json is not parseable: %v\n%s", err, res.stdout)
	}
	if len(doc.Checkpoints) != 1 {
		t.Fatalf("checkpoints = %+v, want exactly one", doc.Checkpoints)
	}
	imgs := doc.Checkpoints[0].Images
	if imgs.Diff != "diff/shots/cart.png" {
		t.Errorf("images.diff = %q, want %q", imgs.Diff, "diff/shots/cart.png")
	}
	if imgs.Overlay != "overlay/shots/cart.png" {
		t.Errorf("images.overlay = %q, want %q", imgs.Overlay, "overlay/shots/cart.png")
	}
	// A and B are each run's OWN capture path, resolved against that run's
	// directory — they are not written by Build and must not be rewritten
	// into the diff layout.
	if imgs.A != "shots/cart.png" || imgs.B != "shots/cart.png" {
		t.Errorf("images.a/b = %q/%q, want the capture's own %q on both sides", imgs.A, imgs.B, "shots/cart.png")
	}
}

// TestImagesFalseWritesNoCheckpointImages is the mirror: an assertion that
// only ever checks the images ARE written would pass against a --images
// hardcoded to true just as happily as against one that is wired.
func TestImagesFalseWritesNoCheckpointImages(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, "app: web\nwire_rules:\n  - headers:\n      date: http-date\n")

	aID := runShot(t, bin, cwd, "web", "checkout", upstream.URL, "shot-a")
	bID := runShot(t, bin, cwd, "web", "checkout", upstream.URL, "shot-b")
	bDir := runDirOf(t, cwd, "web", "checkout", bID)

	res := runRetrace(t, bin, cwd, "", "diff", "--flow", "checkout", "--app", "web",
		"--a", aID, "--b", bID, "--images=false", "--json")
	if res.code != 1 {
		t.Fatalf("exit = %d, want 1\nstdout: %s\nstderr: %s", res.code, res.stdout, res.stderr)
	}
	if _, err := os.Stat(filepath.Join(bDir, "diff", "shots", "cart.png")); err == nil {
		t.Errorf("--images=false still wrote diff/shots/cart.png")
	}
	if strings.Contains(res.stdout, "diff/shots/cart.png") {
		t.Errorf("--images=false still reported an image path:\n%s", res.stdout)
	}
}

// TestOutRedirectsTheCheckpointImages pins --out itself: the paths inside
// the Summary stay relative to OutDir, so a caller pointing --out somewhere
// else gets the same two relative paths under a different root.
func TestOutRedirectsTheCheckpointImages(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, "app: web\nwire_rules:\n  - headers:\n      date: http-date\n")

	aID := runShot(t, bin, cwd, "web", "checkout", upstream.URL, "shot-a")
	bID := runShot(t, bin, cwd, "web", "checkout", upstream.URL, "shot-b")
	bDir := runDirOf(t, cwd, "web", "checkout", bID)
	outDir := t.TempDir()

	res := runRetrace(t, bin, cwd, "", "diff", "--flow", "checkout", "--app", "web",
		"--a", aID, "--b", bID, "--out", outDir)
	if res.code != 1 {
		t.Fatalf("exit = %d, want 1\nstdout: %s\nstderr: %s", res.code, res.stdout, res.stderr)
	}
	if _, err := os.Stat(filepath.Join(outDir, "diff", "shots", "cart.png")); err != nil {
		t.Errorf("--out did not receive the diff image: %v", err)
	}
	if _, err := os.Stat(filepath.Join(bDir, "diff", "shots", "cart.png")); err == nil {
		t.Errorf("--out was given, yet side B's run directory was written to anyway")
	}
}

// --- local helpers ---------------------------------------------------------

// runDirOf resolves one recorded run's directory on disk.
func runDirOf(t *testing.T, cwd, app, flow, id string) string {
	t.Helper()
	p, err := runs.PathsFor(runs.RunsRoot(cwd), app, flow, id)
	if err != nil {
		t.Fatalf("PathsFor(%s/%s/%s): %v", app, flow, id, err)
	}
	return p.RunDir
}

// runBrokenRecording records a run whose test command never touches
// RETRACE_PROXY_URL and posts no markers, so retrace sees not a single
// request of any kind and capture.Assess rates the capture VerdictBroken —
// the same fixture TestRunBannersANonOkVerdict and
// TestDiffExitsThreeOnAQuarantinedSide use. It returns the run id.
func runBrokenRecording(t *testing.T, bin, cwd, upstreamURL string) string {
	t.Helper()
	settlePastRunIDResolution()
	before := map[string]bool{}
	for _, id := range runs.ListRuns(runs.RunsRoot(cwd), "web", "checkout") {
		before[id] = true
	}
	res := runRetrace(t, bin, cwd, "", "run", "--flow", "checkout", "--app", "web",
		"--upstream", upstreamURL, "--", "true")
	if res.code != 0 {
		t.Fatalf("retrace run: exit = %d\nstdout: %s\nstderr: %s", res.code, res.stdout, res.stderr)
	}
	for _, id := range runs.ListRuns(runs.RunsRoot(cwd), "web", "checkout") {
		if !before[id] {
			return id
		}
	}
	t.Fatal("no run directory produced by the broken recording")
	return ""
}

// TestEachSelectorDefaultsToItsOwnSideOfTheComparison pins the two `--a` /
// `--b` default strings and the two `Kind == "none"` guards — four arms that
// every other CLI test walks straight past, because they all pass both
// selectors explicitly.
//
// The defaults are asymmetric on purpose: `--a` is the accepted REFERENCE
// and `--b` is the LATEST run. Swapping them, or dropping either guard, is
// invisible to a test that names both sides. The contract pinned here is
// that "none" is refused with a message telling the operator what to do —
// not passed into Build to fail later as an unreadable directory.
//
// Task 11 made "reference" resolve for real (refs.Resolve: the committed
// bundle, else the newest ELIGIBLE run), so producing a "none" now takes a
// flow whose runs are all ineligible. `git init` with nothing committed is
// the cheapest honest way: every run records Git.Dirty, and a dirty tree is
// not reproducible from a sha. That is a real ineligibility reason, not a
// stub — which is the point of keeping this test after the stub is gone.
func TestEachSelectorDefaultsToItsOwnSideOfTheComparison(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, "app: web\n")
	gitInitDirty(t, cwd)
	aID := runOnce(t, bin, cwd, "web", "checkout", upstream.URL)
	runOnce(t, bin, cwd, "web", "checkout", upstream.URL) // the "latest" run

	t.Run("side A defaults to the reference, which is refused by name", func(t *testing.T) {
		// --b named, --a left to its default.
		res := runRetrace(t, bin, cwd, "", "diff", "--flow", "checkout", "--app", "web", "--b", aID)
		if res.code != 3 {
			t.Fatalf("exit = %d, want 3\nstdout: %s\nstderr: %s", res.code, res.stdout, res.stderr)
		}
		if !strings.Contains(res.stderr, "no reference bundle") {
			t.Fatalf("stderr = %q, want the no-reference-bundle refusal — side A's default must be resolved and refused by name, not carried into Build as an empty run", res.stderr)
		}
	})

	t.Run("side B refuses an unresolvable selector by name too", func(t *testing.T) {
		// The mirror of the first case: the guard on side B is a separate
		// `if`, and dropping it is invisible unless some test asks side B
		// for the one selector that resolves to "none".
		res := runRetrace(t, bin, cwd, "", "diff", "--flow", "checkout", "--app", "web", "--a", aID, "--b", "reference")
		if res.code != 3 {
			t.Fatalf("exit = %d, want 3\nstdout: %s\nstderr: %s", res.code, res.stdout, res.stderr)
		}
		if !strings.Contains(res.stderr, "no reference bundle") {
			t.Fatalf("stderr = %q, want the no-reference-bundle refusal on side B as well as side A", res.stderr)
		}
	})

	t.Run("side B defaults to the latest run, which resolves", func(t *testing.T) {
		// --a named, --b left to its default. If --b defaulted to the
		// reference this would be the same 3 as above.
		res := runRetrace(t, bin, cwd, "", "diff", "--flow", "checkout", "--app", "web", "--a", aID)
		if res.code == 3 {
			t.Fatalf("exit = 3 — side B's default must resolve to the latest run, not to the reference\nstdout: %s\nstderr: %s", res.stdout, res.stderr)
		}
		if strings.Contains(res.stderr, "no reference bundle") {
			t.Fatalf("stderr = %q, want a real comparison against the latest run", res.stderr)
		}
	})
}

// gitInitDirty makes cwd a git repository with nothing committed, so
// capture.GitInfo records Git.Dirty == true for every run recorded in it.
// A dirty tree is not reproducible from a sha, so refs.Resolve rules every
// such run ineligible and reports "none" — which is how a test reaches the
// "no reference" arm now that the selector genuinely resolves.
func gitInitDirty(t *testing.T, cwd string) {
	t.Helper()
	if out, err := exec.Command("git", "-C", cwd, "init").CombinedOutput(); err != nil {
		t.Skipf("git init unavailable: %v\n%s", err, out)
	}
	if out, err := exec.Command("git", "-C", cwd, "status", "--porcelain").Output(); err != nil || len(out) == 0 {
		t.Fatalf("expected an untracked-file dirty tree, got %q (err %v)", out, err)
	}
}
