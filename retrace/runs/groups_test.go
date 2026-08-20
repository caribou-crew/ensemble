package runs

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func ts(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestDeriveGroupsClosesOnNextStartAndAtFinish(t *testing.T) {
	records := []GroupRecord{
		{Phase: "start", Name: "browse", TS: ts("2026-08-21T10:00:00Z")},
		{Phase: "start", Name: "checkout", TS: ts("2026-08-21T10:00:10Z")},
		{Phase: "end", TS: ts("2026-08-21T10:00:20Z")},
		{Phase: "start", Name: "receipt", TS: ts("2026-08-21T10:00:30Z")},
	}
	got := DeriveGroups(records, ts("2026-08-21T10:00:40Z"))
	if len(got) != 3 {
		t.Fatalf("want 3 intervals, got %d: %+v", len(got), got)
	}
	if got[0].Name != "browse" || !got[0].EndedAt.Equal(ts("2026-08-21T10:00:10Z")) {
		t.Fatalf("an unclosed group must end when the next one starts: %+v", got[0])
	}
	if !got[2].EndedAt.Equal(ts("2026-08-21T10:00:40Z")) {
		t.Fatalf("the last open group must close at finishedAt: %+v", got[2])
	}
}

func TestDeriveGroupsSortsOutOfOrderRecords(t *testing.T) {
	records := []GroupRecord{
		{Phase: "start", Name: "second", TS: ts("2026-08-21T10:00:10Z")},
		{Phase: "start", Name: "first", TS: ts("2026-08-21T10:00:00Z")},
	}
	got := DeriveGroups(records, ts("2026-08-21T10:00:20Z"))
	if got[0].Name != "first" {
		t.Fatalf("records must be sorted by ts, got %+v", got)
	}
}

func TestGroupAtIsHalfOpen(t *testing.T) {
	groups := DeriveGroups([]GroupRecord{
		{Phase: "start", Name: "a", TS: ts("2026-08-21T10:00:00Z")},
		{Phase: "start", Name: "b", TS: ts("2026-08-21T10:00:10Z")},
	}, ts("2026-08-21T10:00:20Z"))

	if got := GroupAt(groups, ts("2026-08-21T10:00:10Z")); got != "b" {
		t.Fatalf("a boundary call joins the group that just opened, got %q", got)
	}
	if got := GroupAt(groups, ts("2026-08-21T09:59:59Z")); got != "" {
		t.Fatalf("before any group must be empty, got %q", got)
	}
}

func TestAppendAndReadGroupRecordsSkipsCorruptLines(t *testing.T) {
	p, err := Create(RunsRoot(t.TempDir()), "web", "checkout", "r1")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := AppendGroupRecord(p, GroupRecord{Phase: "start", Name: "a", TS: ts("2026-08-21T10:00:00Z")}); err != nil {
		t.Fatalf("AppendGroupRecord: %v", err)
	}
	if err := appendRaw(p.RunDir, "{not json\n"); err != nil {
		t.Fatalf("appendRaw: %v", err)
	}
	if err := AppendGroupRecord(p, GroupRecord{Phase: "end", TS: ts("2026-08-21T10:00:05Z")}); err != nil {
		t.Fatalf("AppendGroupRecord: %v", err)
	}
	got, err := ReadGroupRecords(p)
	if err != nil {
		t.Fatalf("ReadGroupRecords: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("a corrupt marker line must be dropped, not fatal: %+v", got)
	}
}

// TestGroupFunctionsTakeAPathsNotAnOpaqueString — review finding 2
// (Critical), re-review section 2, write side. AppendGroupRecord and
// ReadGroupRecords used to accept a bare runDir string with no validation
// and no doc comment saying it had to come from PathsFor/Create — the
// review named this as exactly the shape a Task 4 marker-door handler
// would wire RETRACE_RUN_DIR or a request field into with no guard at all.
// They now require a Paths, which is only obtainable from PathsFor/Create
// — both of which validate app/flow/runID (see TestPathsForRejectsTraversal
// in paths_test.go). A Paths{RunDir: ...} literal built by hand (as this
// test does) is still technically forgeable — that is an accepted,
// documented residual (see Paths' and AppendGroupRecord's doc comments),
// not a hole this round closes. What this test pins is the structural
// guarantee itself: the only way a caller reaches these functions is
// through a value shaped like Paths, so a caller who goes through the
// sanctioned constructors (PathsFor/Create) cannot reach them with an
// escaping RunDir at all.
func TestGroupFunctionsTakeAPathsNotAnOpaqueString(t *testing.T) {
	tmp := t.TempDir()
	root := RunsRoot(filepath.Join(tmp, "proj"))
	esc := filepath.Join("..", "..", "..", "outside")
	if _, err := PathsFor(root, esc, "SECRET", "r1"); err == nil {
		t.Fatal("PathsFor must reject a traversal component before any Paths carrying that RunDir could reach AppendGroupRecord/ReadGroupRecords")
	}

	// The accepted residual: a hand-built Paths literal is not rejected —
	// only the sanctioned constructors are guarded.
	forged := Paths{RunDir: filepath.Join(tmp, "forged-outside-run")}
	if err := AppendGroupRecord(forged, GroupRecord{Phase: "start", Name: "a", TS: ts("2026-08-21T10:00:00Z")}); err != nil {
		t.Fatalf("AppendGroupRecord with a forged Paths: %v", err)
	}
	if _, err := os.Stat(filepath.Join(forged.RunDir, "groups.jsonl")); err != nil {
		t.Fatalf("expected the forged RunDir to have been written to (documented residual, not a regression): %v", err)
	}
}

func appendRaw(runDir, line string) error {
	f, err := os.OpenFile(filepath.Join(runDir, "groups.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(line)
	return err
}
