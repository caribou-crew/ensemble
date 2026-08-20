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
