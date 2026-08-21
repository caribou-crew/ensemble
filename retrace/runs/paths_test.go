package runs

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewRunIDIsLexicallyChronological(t *testing.T) {
	t0 := time.Date(2026, 8, 21, 10, 15, 0, 0, time.UTC)
	a := NewRunID(t0, "ab12cd3ef")
	b := NewRunID(t0.Add(time.Second), "ab12cd3ef")
	if a != "20260821T101500Z-ab12cd3" {
		t.Fatalf("run id = %q, want 20260821T101500Z-ab12cd3", a)
	}
	if !(a < b) {
		t.Fatalf("run ids must sort chronologically: %q !< %q", a, b)
	}
}

func TestNewRunIDWithoutShaStillUnique(t *testing.T) {
	t0 := time.Date(2026, 8, 21, 10, 15, 0, 0, time.UTC)
	if got := NewRunID(t0, ""); got != "20260821T101500Z-nogit" {
		t.Fatalf("run id = %q, want 20260821T101500Z-nogit", got)
	}
}

func TestCreateMakesShotsDirAndListingsRoundTrip(t *testing.T) {
	root := RunsRoot(t.TempDir())
	p, err := Create(root, "web", "checkout", "20260821T101500Z-ab12cd3")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if st, err := os.Stat(p.ShotsDir); err != nil || !st.IsDir() {
		t.Fatalf("shots dir not created: %v", err)
	}
	if p.WirePath != filepath.Join(p.RunDir, "wire.jsonl") {
		t.Fatalf("WirePath = %q", p.WirePath)
	}
	if got := ListApps(root); len(got) != 1 || got[0] != "web" {
		t.Fatalf("ListApps = %v", got)
	}
	if got := ListFlows(root, "web"); len(got) != 1 || got[0] != "checkout" {
		t.Fatalf("ListFlows = %v", got)
	}
	if got := ListRuns(root, "web", "checkout"); len(got) != 1 {
		t.Fatalf("ListRuns = %v", got)
	}
}

func TestFindRunSelectors(t *testing.T) {
	root := RunsRoot(t.TempDir())
	for _, id := range []string{"20260821T100000Z-aaa1111", "20260821T110000Z-bbb2222"} {
		if _, err := Create(root, "web", "checkout", id); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}
	if got := FindRun(root, "web", "checkout", "latest"); got != "20260821T110000Z-bbb2222" {
		t.Fatalf("latest = %q", got)
	}
	if got := FindRun(root, "web", "checkout", "aaa1111"); got != "20260821T100000Z-aaa1111" {
		t.Fatalf("by sha = %q", got)
	}
	if got := FindRun(root, "web", "checkout", "nope"); got != "" {
		t.Fatalf("unknown selector = %q, want empty", got)
	}
}

func TestListingsOfMissingRootAreEmptyNotPanic(t *testing.T) {
	root := filepath.Join(t.TempDir(), "never-created")
	if got := ListApps(root); len(got) != 0 {
		t.Fatalf("ListApps = %v", got)
	}
	if got := ListRuns(root, "web", "checkout"); len(got) != 0 {
		t.Fatalf("ListRuns = %v", got)
	}
}

// TestPathsForRejectsTraversal — review finding 2 (Critical). app/flow/runID
// can all originate from an HTTP request (Task 13's review server routes
// /api/runs/{app}/{flow}/{run}/... straight into PathsFor), and
// net/http.ServeMux cleans the path AFTER routing on the still-escaped
// string, so a component containing ".." must never reach filepath.Join.
func TestPathsForRejectsTraversal(t *testing.T) {
	root := RunsRoot(t.TempDir())
	cases := []struct {
		name, app, flow, runID string
	}{
		{"dot-dot app", "..", "checkout", "r1"},
		{"dot-dot flow", "web", "..", "r1"},
		{"dot-dot runID escapes to etc", "web", "checkout", "../../../../etc/pwn"},
		{"embedded dot-dot escapes root", "web", "checkout", "../../../../escaped"},
		{"embedded separator", "web", "che/ckout", "r1"},
		{"leading dot", "web", ".checkout", "r1"},
		{"bare dot-dot everywhere", "..", "..", ".."},
		{"empty component", "web", "", "r1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := PathsFor(root, c.app, c.flow, c.runID); err == nil {
				t.Fatalf("PathsFor(%q,%q,%q) = nil error, want rejection", c.app, c.flow, c.runID)
			}
		})
	}
}

func TestPathsForAcceptsRefRunID(t *testing.T) {
	root := RefsRoot(t.TempDir())
	if _, err := PathsFor(root, "web", "checkout", RefRunID); err != nil {
		t.Fatalf("PathsFor with RefRunID must be accepted: %v", err)
	}
}

// TestCreatePropagatesPathsForRejection — a rejected Create must leave no
// trace anywhere the runs root can be listed from.
func TestCreatePropagatesPathsForRejection(t *testing.T) {
	root := RunsRoot(t.TempDir())
	if _, err := Create(root, "web", "checkout", "../../../../escaped"); err == nil {
		t.Fatal("Create must reject a traversal run id, not create a directory outside root")
	}
	if got := ListApps(root); len(got) != 0 {
		t.Fatalf("a rejected Create must leave no trace: ListApps = %v", got)
	}
}

// TestCreateFailsOnRunIDCollision — review finding 3 (Major). Two runs must
// never silently share one directory: the second Create with the same run
// id must fail, not adopt the first run's files.
func TestCreateFailsOnRunIDCollision(t *testing.T) {
	root := RunsRoot(t.TempDir())
	if _, err := Create(root, "web", "checkout", "same-id"); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	if _, err := Create(root, "web", "checkout", "same-id"); err == nil {
		t.Fatal("a second Create with the same run id must fail, not silently merge into the first run's directory")
	}
}

// TestSiblingsHonorTheGuard — review finding 2 (Critical), re-review
// section 2. validateComponent was applied at PathsFor's single call site
// only; every other exported function in this package that joins a
// caller-supplied component into a path had no guard at all. Moved in from
// the controller's probe (.superpowers/sdd/2026-08-21-phase-4-retrace/
// probes/siblings_guard_probe_test.go), which measured against 7e5d5f3:
//
//	ListFlows(root, "../../..")            -> [outside proj]
//	ListRuns(root, "../../..", "outside")  -> [secret]
//	ListFlowsErr(root, "../../..")         -> ([outside proj], <nil>)
//	FindRun(root, "../../..", "outside", "latest") -> "secret"
//
// Extended here (the probe did not reach it) to cover ListRunsErr, the
// other lister with no error-channel excuse for skipping validation.
//
// The decoy tree has a child under SECRET that looks like a real run id
// (re-review round 3): with SECRET childless, ListRuns(root, esc,
// "SECRET") and FindRun(root, esc, "SECRET", "latest") return empty
// whether or not their guard exists, so those two assertions were vacuous
// — deleting either guard kept this test green. A child directory gives
// both something to find if the guard is missing.
func TestSiblingsHonorTheGuard(t *testing.T) {
	tmp := t.TempDir()
	root := RunsRoot(filepath.Join(tmp, "proj"))
	outside := filepath.Join(tmp, "outside")
	if err := os.MkdirAll(filepath.Join(outside, "SECRET", "20260101T000000Z-deadbee"), 0o755); err != nil {
		t.Fatal(err)
	}
	esc := filepath.Join("..", "..", "..", "outside")

	if got := ListFlows(root, esc); len(got) > 0 {
		t.Errorf("ListFlows escaped the runs root: %v", got)
	}
	if got, err := ListFlowsErr(root, esc); len(got) > 0 || err == nil {
		t.Errorf("ListFlowsErr escaped the runs root: %v (err=%v)", got, err)
	}
	if got := ListRuns(root, esc, "SECRET"); len(got) > 0 {
		t.Errorf("ListRuns escaped the runs root: %v", got)
	}
	if got, err := ListRunsErr(root, esc, "SECRET"); len(got) > 0 || err == nil {
		t.Errorf("ListRunsErr escaped the runs root: %v (err=%v)", got, err)
	}
	if got := FindRun(root, esc, "SECRET", "latest"); got != "" {
		t.Errorf("FindRun escaped the runs root: %q", got)
	}
}

// TestFindRunDoesNotValidateSelector — FindRun validates app/flow (both are
// path components) but must not apply the same component rules to selector:
// a selector is "latest", an index, or a run-id prefix, never a path
// component, so a legitimate short-sha selector containing characters
// outside validComponent's charset (there are none in practice, but the
// point is the rule set is different) must still resolve. FindRun never
// joins selector into a filesystem path itself — it only compares it
// against run ids already read from the (validated) root/app/flow
// directory — so no separate traversal guard is needed for it here.
//
// The short-sha assertion alone cannot detect this property (re-review
// round 3): "aaa1111" is itself a valid component, so it resolves
// identically whether or not FindRun validates selector — the test passed
// just as happily against a FindRun that added selector to
// validateComponents. "" is a selector the component rules WOULD reject
// (validateComponent rejects empty names), and FindRun's own contract
// requires "" to mean "latest", so asserting on it is a selector value
// the two behaviors actually disagree on.
func TestFindRunDoesNotValidateSelector(t *testing.T) {
	root := RunsRoot(t.TempDir())
	if _, err := Create(root, "web", "checkout", "20260821T100000Z-aaa1111"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got := FindRun(root, "web", "checkout", "aaa1111"); got != "20260821T100000Z-aaa1111" {
		t.Fatalf("FindRun by short sha = %q", got)
	}
	if got := FindRun(root, "web", "checkout", ""); got != "20260821T100000Z-aaa1111" {
		t.Fatalf(`FindRun with an empty selector = %q, want the latest run — "" must mean "latest", not "invalid component"`, got)
	}
}

// TestListAppsErrDistinguishesMissingFromBroken — review finding 7 (Major).
// A never-written root is legitimately empty; a root that exists but can't
// be read (wrong permissions, a broken mount, or — as tested here — a
// regular file where a directory is expected) must surface as an error,
// not silently report "no runs".
func TestListAppsErrDistinguishesMissingFromBroken(t *testing.T) {
	dir := t.TempDir()

	missing := filepath.Join(dir, "never-created")
	if got, err := ListAppsErr(missing); err != nil || len(got) != 0 {
		t.Fatalf("ListAppsErr(missing) = (%v, %v), want (nil, nil)", got, err)
	}

	notADir := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(notADir, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := ListAppsErr(notADir); err == nil {
		t.Fatal("ListAppsErr must surface a real error when root is not a directory")
	}
}

// TestValidateComponentsIsTheSameGuardPathsForUses pins R-A's exported
// wrapper to the ONE guard body. It asserts agreement with PathsFor rather
// than re-listing a charset: a second copy of the rule in a test is how the
// two silently diverge, which is the exact failure the wrapper exists to
// prevent. Every case PathsFor rejects, ValidateComponents must reject, and
// every case it accepts, ValidateComponents must accept.
func TestValidateComponentsIsTheSameGuardPathsForUses(t *testing.T) {
	root := RunsRoot(t.TempDir())
	names := []string{
		"web", "checkout", RefRunID, "20260821T101500Z-abc1234",
		"..", ".", "", ".hidden", "a/b", `a\b`, "../../etc/pwn", "bad name", "sémantique",
	}
	for _, n := range names {
		t.Run(n, func(t *testing.T) {
			_, pathsErr := PathsFor(root, n, "flow", "run")
			wrapErr := ValidateComponents(n)
			if (pathsErr == nil) != (wrapErr == nil) {
				t.Fatalf("ValidateComponents(%q) = %v but PathsFor's guard = %v — the two must be one rule", n, wrapErr, pathsErr)
			}
		})
	}
}

// TestValidateComponentsChecksEveryArgument — a variadic guard that only
// looks at its first argument would pass every single-component test above
// and still let BundleDir join an unvalidated flow.
func TestValidateComponentsChecksEveryArgument(t *testing.T) {
	if err := ValidateComponents("web", ".."); err == nil {
		t.Fatal("ValidateComponents(\"web\", \"..\") = nil, want a rejection naming the second component")
	}
	if err := ValidateComponents("..", "checkout"); err == nil {
		t.Fatal("ValidateComponents(\"..\", \"checkout\") = nil, want a rejection")
	}
	if err := ValidateComponents(); err != nil {
		t.Fatalf("ValidateComponents() with no names = %v, want nil — nothing to reject", err)
	}
}
