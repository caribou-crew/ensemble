package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/caribou-crew/ensemble/retrace/runs"
)

// TestSplitSelectorReadsTheAppOffTheSelector covers the parse alone. The
// separator choice is only safe because run ids and shas never contain "@"
// and runs.validateComponents forbids it in an app name, so the cases that
// matter are the ones where it is absent or leading.
func TestSplitSelectorReadsTheAppOffTheSelector(t *testing.T) {
	for _, tc := range []struct {
		in               string
		wantApp, wantSel string
	}{
		{"latest", "web", "latest"},
		{"mobile@latest", "mobile", "latest"},
		{"mobile@2026-08-01T00-00-00Z-abc1234", "mobile", "2026-08-01T00-00-00Z-abc1234"},
		{"mobile@reference", "mobile", "reference"},
		// A leading separator reads as the default app, so the failure the
		// user sees is about the run they named and not about component
		// validation of an empty string.
		{"@latest", "web", "latest"},
		// Cut at the FIRST separator: a stray one inside a selector stays in
		// the selector and fails to find a run, rather than silently becoming
		// a different app.
		{"mobile@lat@est", "mobile", "lat@est"},
	} {
		t.Run(tc.in, func(t *testing.T) {
			app, sel := splitSelector(tc.in, "web")
			if app != tc.wantApp || sel != tc.wantSel {
				t.Errorf("splitSelector(%q) = (%q, %q), want (%q, %q)", tc.in, app, sel, tc.wantApp, tc.wantSel)
			}
		})
	}
}

func TestRepeatedRootsAreAbsoluteAndDeduplicated(t *testing.T) {
	var r rootList
	if err := r.Set("."); err != nil {
		t.Fatal(err)
	}
	if len(r) != 1 || !strings.HasPrefix(r[0], "/") {
		t.Fatalf("roots = %v, want one absolute path", r)
	}
	// The same directory named two ways is a script being careful, not a
	// mistake — and keeping both would make every lookup ambiguous against
	// itself, which is the one error this flag must never invent.
	if err := r.Set(r[0]); err != nil {
		t.Fatal(err)
	}
	if len(r) != 1 {
		t.Errorf("roots = %v after re-adding the same directory, want one", r)
	}
	if err := r.Set("   "); err == nil {
		t.Error("an empty --root was accepted")
	}
}

func TestNoRootFlagMeansTheWorkingDirectory(t *testing.T) {
	// The single-root path and the many-roots path must be ONE code path, or
	// every existing behaviour gets a second implementation to drift from.
	var r rootList
	got := r.resolve("/somewhere")
	if len(got) != 1 || got[0] != "/somewhere" {
		t.Errorf("resolve = %v, want [/somewhere]", got)
	}
}

// TestDiffComparesTwoRunsFromDifferentRepositories is the section's point:
// two clients, two repositories, one comparison. Without --root, `diff` can
// only see the tree it was invoked from, and the question "did the stack
// change or did my client?" is hardest to answer in exactly the situation
// where two clients hit the same backend.
func TestDiffComparesTwoRunsFromDifferentRepositories(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	bin := buildRetrace(t)
	webRepo, mobileRepo := t.TempDir(), t.TempDir()
	writeConfig(t, webRepo, "app: web\n")
	writeConfig(t, mobileRepo, "app: mobile\n")

	runOnce(t, bin, webRepo, "web", "checkout", upstream.URL)
	runOnce(t, bin, mobileRepo, "mobile", "checkout", upstream.URL)

	res := runRetrace(t, bin, webRepo, "fetch",
		"diff", "--flow", "checkout", "--json", "--images=false",
		"--root", webRepo, "--root", mobileRepo,
		"--a", "web@latest", "--b", "mobile@latest")
	if res.code != 0 && res.code != 1 {
		t.Fatalf("exit = %d, want 0 or 1 (a real comparison)\nstdout: %s\nstderr: %s", res.code, res.stdout, res.stderr)
	}

	var got struct {
		A struct {
			RunID    string `json:"runId"`
			Manifest struct {
				App string `json:"app"`
			} `json:"manifest"`
		} `json:"a"`
		B struct {
			RunID    string `json:"runId"`
			Manifest struct {
				App string `json:"app"`
			} `json:"manifest"`
		} `json:"b"`
	}
	if err := json.Unmarshal([]byte(res.stdout), &got); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, res.stdout)
	}
	// Each side's own app comes off its own manifest — the two sides of a
	// cross-repo diff are not the same app, and nothing in the document has
	// to be told so separately.
	if got.A.Manifest.App != "web" {
		t.Errorf("side a app = %q, want web", got.A.Manifest.App)
	}
	if got.B.Manifest.App != "mobile" {
		t.Errorf("side b app = %q, want mobile", got.B.Manifest.App)
	}
	if got.A.RunID == "" || got.B.RunID == "" || got.A.RunID == got.B.RunID {
		t.Errorf("sides resolved to %q and %q — want two distinct runs from two trees", got.A.RunID, got.B.RunID)
	}
}

// TestASelectorMatchingTwoRootsIsRefused is the failure this flag creates and
// must not resolve silently. Two checkouts of the same repository is what
// anyone comparing a branch against main has, and first-wins would produce a
// diff that is honestly labelled and completely wrong.
func TestASelectorMatchingTwoRootsIsRefused(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	bin := buildRetrace(t)
	main, branch := t.TempDir(), t.TempDir()
	cfg := "app: web\n"
	writeConfig(t, main, cfg)
	writeConfig(t, branch, cfg)
	runOnce(t, bin, main, "web", "checkout", upstream.URL)
	runOnce(t, bin, branch, "web", "checkout", upstream.URL)

	res := runRetrace(t, bin, main, "fetch",
		"diff", "--flow", "checkout", "--images=false",
		"--root", main, "--root", branch,
		"--a", "web@latest", "--b", "web@latest")
	if res.code == 0 {
		t.Fatalf("an ambiguous selector produced a diff\nstdout: %s", res.stdout)
	}
	if !strings.Contains(res.stderr, "more than one root") {
		t.Errorf("the refusal does not say what is ambiguous: %s", res.stderr)
	}
	for _, want := range []string{main, branch} {
		if !strings.Contains(res.stderr, want) {
			t.Errorf("the refusal does not name %s: %s", want, res.stderr)
		}
	}
}

// TestRunsListsAcrossRootsAndSaysWhichTreeEachCameFrom: with several trees,
// app/flow/run no longer identifies a run — two checkouts of one repository
// produce runs with the same app, the same flow, and occasionally the same
// id — so the root belongs on the row.
func TestRunsListsAcrossRootsAndSaysWhichTreeEachCameFrom(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	bin := buildRetrace(t)
	webRepo, mobileRepo := t.TempDir(), t.TempDir()
	writeConfig(t, webRepo, "app: web\n")
	writeConfig(t, mobileRepo, "app: mobile\n")
	runOnce(t, bin, webRepo, "web", "checkout", upstream.URL)
	runOnce(t, bin, mobileRepo, "mobile", "checkout", upstream.URL)

	res := runRetrace(t, bin, webRepo, "fetch", "runs", "--json", "--root", webRepo, "--root", mobileRepo)
	if res.code != 0 {
		t.Fatalf("exit = %d\nstderr: %s", res.code, res.stderr)
	}
	var got struct {
		Root  string   `json:"root"`
		Roots []string `json:"roots"`
		Runs  []struct {
			App  string `json:"app"`
			Root string `json:"root"`
		} `json:"runs"`
	}
	if err := json.Unmarshal([]byte(res.stdout), &got); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, res.stdout)
	}
	if len(got.Roots) != 2 {
		t.Errorf("roots = %v, want both", got.Roots)
	}
	// The single-root field is the first of the list and speaks the same
	// vocabulary as it: a repository directory, the value you hand back to
	// --root. Two fields a letter apart meaning two different paths is a bug
	// a consumer finds after shipping.
	if len(got.Roots) > 0 && got.Root != got.Roots[0] {
		t.Errorf("root = %q but roots[0] = %q — the two disagree", got.Root, got.Roots[0])
	}
	if len(got.Runs) != 2 {
		t.Fatalf("runs = %+v, want one from each tree", got.Runs)
	}
	byApp := map[string]string{}
	for _, r := range got.Runs {
		byApp[r.App] = r.Root
	}
	if byApp["web"] != webRepo {
		t.Errorf("web run reports root %q, want %s", byApp["web"], webRepo)
	}
	if byApp["mobile"] != mobileRepo {
		t.Errorf("mobile run reports root %q, want %s", byApp["mobile"], mobileRepo)
	}

	// And in the text listing, where the column only appears when there is
	// more than one root to tell apart.
	text := runRetrace(t, bin, webRepo, "fetch", "runs", "--root", webRepo, "--root", mobileRepo)
	if !strings.Contains(text.stdout, "ROOT") {
		t.Errorf("the multi-root table has no ROOT column:\n%s", text.stdout)
	}
	single := runRetrace(t, bin, webRepo, "fetch", "runs", "--root", webRepo)
	if strings.Contains(single.stdout, "ROOT") {
		t.Errorf("a single-root table spends a column on one repeated value:\n%s", single.stdout)
	}
}

// TestAnUnreadableRootFailsTheWholeListing: a partial listing that still
// exits 0 answers a question about N trees with an answer about fewer, and
// nothing in the exit code says so. A missing root is NOT this case — a tree
// that has never recorded a run is empty, not broken — so the test has to
// build a root that genuinely cannot be read.
func TestAnUnreadableRootFailsTheWholeListing(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	bin := buildRetrace(t)
	good, bad := t.TempDir(), t.TempDir()
	writeConfig(t, good, "app: web\n")
	writeConfig(t, bad, "app: mobile\n")
	runOnce(t, bin, good, "web", "checkout", upstream.URL)

	sealed := runs.RunsRoot(bad)
	if err := os.MkdirAll(sealed, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sealed, 0o000); err != nil {
		t.Fatal(err)
	}
	// Restore before TempDir's cleanup, which cannot remove what it cannot
	// traverse.
	t.Cleanup(func() { os.Chmod(sealed, 0o755) })
	if _, err := os.ReadDir(sealed); err == nil {
		t.Skip("this user can read a 0000 directory; there is no unreadable root to build")
	}

	res := runRetrace(t, bin, good, "fetch", "runs", "--json", "--root", good, "--root", bad)
	if res.code == 0 {
		t.Fatalf("an unreadable root produced a clean listing — a caller asking about two trees was answered about one\nstdout: %s", res.stdout)
	}
	if !strings.Contains(res.stderr, sealed) {
		t.Errorf("the failure does not name the root it could not read: %s", res.stderr)
	}
}

// TestRunsInterleavesRootsByIdentityNotByArgumentOrder: StatusAll sorts
// within one root, so a concatenation across roots is only grouped. A listing
// whose order tracks the order the --root flags happened to be typed is one
// nobody can diff between two invocations.
func TestRunsInterleavesRootsByIdentityNotByArgumentOrder(t *testing.T) {
	bin := buildRetrace(t)
	first, second := t.TempDir(), t.TempDir()
	writeConfig(t, first, "app: web\n")
	writeConfig(t, second, "app: nectar\n")
	// Chosen so the two orders differ: grouped by root it reads
	// mobile, web, nectar; sorted by identity it reads mobile, nectar, web.
	fabricateRun(t, first, "mobile", "checkout", 2*time.Hour, true)
	fabricateRun(t, first, "web", "checkout", 2*time.Hour, true)
	fabricateRun(t, second, "nectar", "checkout", 2*time.Hour, true)

	res := runRetrace(t, bin, first, "", "runs", "--json", "--root", first, "--root", second)
	if res.code != exitOK {
		t.Fatalf("exit = %d\nstderr: %s", res.code, res.stderr)
	}
	got := decodeRuns(t, res.stdout)
	var apps []string
	for _, r := range got.Runs {
		apps = append(apps, r.App)
	}
	if strings.Join(apps, ",") != "mobile,nectar,web" {
		t.Errorf("listing order = %v, want mobile,nectar,web — grouped by root rather than sorted by identity", apps)
	}
}

// TestWithSeveralRootsTheMissingReferenceIsExplainedPerRoot: "there is no
// reference" stops being one fact once there is more than one tree. Each has
// its own reason, and the one the reader needs is whichever tree they thought
// they had accepted a bundle in.
func TestWithSeveralRootsTheMissingReferenceIsExplainedPerRoot(t *testing.T) {
	bin := buildRetrace(t)
	first, second := t.TempDir(), t.TempDir()
	writeConfig(t, first, "app: web\n")
	writeConfig(t, second, "app: web\n")

	res := runRetrace(t, bin, first, "", "diff", "--flow", "checkout", "--images=false",
		"--root", first, "--root", second)
	if res.code == 0 {
		t.Fatalf("a diff with no reference on either side exited 0\nstdout: %s", res.stdout)
	}
	for _, want := range []string{first + ": ", second + ": "} {
		if !strings.Contains(res.stderr, want) {
			t.Errorf("the refusal does not attribute a reason to %s:\n%s", want, res.stderr)
		}
	}

	// And with ONE root it stays a bare reason: prefixing the only tree there
	// is spends a line on a fact the reader already supplied.
	solo := runRetrace(t, bin, first, "", "diff", "--flow", "checkout", "--images=false", "--root", first)
	if solo.code == 0 {
		t.Fatalf("a single-root diff with no reference exited 0\nstdout: %s", solo.stdout)
	}
	if strings.Contains(solo.stderr, first+": ") {
		t.Errorf("a single-root refusal prefixes the only root it searched:\n%s", solo.stderr)
	}
}

// TestCrossAppDiffPersistsAPairingUnderSideBsRunDirectory: the dashboard's
// cross-app compare view (docs/superpowers/specs/2026-09-04-cross-app-compare-view-design.md)
// never recomputes a diff — it only reads what the CLI already persisted.
// This is the CLI half of that contract: a cross-app `retrace diff` must
// leave a pair.json and a summary.json behind, in a pairing directory
// alongside side B's own run, that `retrace serve` can discover with no
// index file.
func TestCrossAppDiffPersistsAPairingUnderSideBsRunDirectory(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	bin := buildRetrace(t)
	webRepo, mobileRepo := t.TempDir(), t.TempDir()
	writeConfig(t, webRepo, "app: web\n")
	writeConfig(t, mobileRepo, "app: mobile\n")

	webRunID := runOnce(t, bin, webRepo, "web", "checkout", upstream.URL)
	mobileRunID := runOnce(t, bin, mobileRepo, "mobile", "checkout", upstream.URL)

	res := runRetrace(t, bin, webRepo, "fetch",
		"diff", "--flow", "checkout", "--json",
		"--root", webRepo, "--root", mobileRepo,
		"--a", "web@latest", "--b", "mobile@latest")
	if res.code != 0 && res.code != 1 {
		t.Fatalf("exit = %d, want 0 or 1 (a real comparison)\nstdout: %s\nstderr: %s", res.code, res.stdout, res.stderr)
	}

	pairDir := filepath.Join(runs.RunsRoot(mobileRepo), "mobile", "checkout", mobileRunID, "diffs", "web__"+webRunID)
	pairJSON, err := os.ReadFile(filepath.Join(pairDir, "pair.json"))
	if err != nil {
		t.Fatalf("no pair.json persisted at %s: %v", pairDir, err)
	}
	var pair struct{ AppA, AppB, RunA, RunB, Verdict string }
	if err := json.Unmarshal(pairJSON, &pair); err != nil {
		t.Fatalf("pair.json is not valid JSON: %v\n%s", err, pairJSON)
	}
	if pair.AppA != "web" || pair.AppB != "mobile" {
		t.Errorf("pair.json apps = %q/%q, want web/mobile", pair.AppA, pair.AppB)
	}
	if pair.RunA != webRunID || pair.RunB != mobileRunID {
		t.Errorf("pair.json runs = %q/%q, want %q/%q", pair.RunA, pair.RunB, webRunID, mobileRunID)
	}
	if _, err := os.Stat(filepath.Join(pairDir, "summary.json")); err != nil {
		t.Errorf("no summary.json persisted at %s: %v", pairDir, err)
	}
}

// TestSameAppDiffPersistsNoPairing: the persistence behavior added for
// cross-app diffs must not change a same-app `retrace diff` at all — no
// pair.json, no diffs/ directory, images written exactly where they always
// were (directly under the run, not nested under diffs/<pairId>).
func TestSameAppDiffPersistsNoPairing(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, "app: web\n")
	runOnce(t, bin, cwd, "web", "checkout", upstream.URL)
	runID := runOnce(t, bin, cwd, "web", "checkout", upstream.URL)

	res := runRetrace(t, bin, cwd, "fetch", "diff", "--flow", "checkout", "--app", "web")
	if res.code != 0 && res.code != 1 {
		t.Fatalf("exit = %d\nstderr: %s", res.code, res.stderr)
	}

	runDir := filepath.Join(runs.RunsRoot(cwd), "web", "checkout", runID)
	if _, err := os.Stat(filepath.Join(runDir, "diffs")); !os.IsNotExist(err) {
		t.Errorf("a same-app diff left a diffs/ directory at %s behind: %v", runDir, err)
	}
}
