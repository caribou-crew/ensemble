package runs

import (
	"errors"
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
	got, skipped, err := ReadGroupRecords(p)
	if err != nil {
		t.Fatalf("ReadGroupRecords: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("a corrupt marker line must be dropped, not fatal: %+v", got)
	}
	if skipped != 1 {
		t.Fatalf("skipped = %d, want 1 — a dropped line the caller is never told about is indistinguishable from a line that was never written", skipped)
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

// --- a marker with no timestamp is not a marker ---------------------------
//
// The three tests below pin one rule at the three seams that can see it.
// The rule matters far out of proportion to its size: TS is a bare
// time.Time on an EXPORTED struct that an exported function takes by
// value, so `GroupRecord{Phase: "start", Name: "warmup", Quiet: true}` —
// one omitted field, ordinary Go — used to become a declared-silent
// interval spanning [0001-01-01, run end) that suppressed gap detection for
// the ENTIRE run. See ErrMarkerWithoutTimestamp.

// TestAppendGroupRecordRefusesAMarkerWithNoTimestamp pins the write seam.
// It also asserts that nothing reached disk: a refusal that still appends
// is a refusal only in the return value.
func TestAppendGroupRecordRefusesAMarkerWithNoTimestamp(t *testing.T) {
	p, err := Create(RunsRoot(t.TempDir()), "web", "checkout", "r1")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Exactly the literal a caller writes when they forget the field —
	// well-formed in every other respect, and `quiet`, which is what makes
	// it expensive.
	err = AppendGroupRecord(p, GroupRecord{Phase: "start", Name: "warmup", Quiet: true})
	if !errors.Is(err, ErrMarkerWithoutTimestamp) {
		t.Fatalf("AppendGroupRecord error = %v, want ErrMarkerWithoutTimestamp — a zero TS is invalid input, not a quiet interval starting at the beginning of time", err)
	}
	if _, statErr := os.Stat(filepath.Join(p.RunDir, "groups.jsonl")); !os.IsNotExist(statErr) {
		body, _ := os.ReadFile(filepath.Join(p.RunDir, "groups.jsonl"))
		t.Fatalf("the refused marker was written anyway: %q", body)
	}
}

// TestReadGroupRecordsDropsAndCountsAMarkerWithNoTimestamp pins the read
// seam, which is the one a third-party file-drop reaches: `groups.jsonl` is
// a documented protocol (adapters/js/README.md), and a line with no `ts` is
// VALID JSON, so the fail-open reader honoured it where it would have
// dropped a corrupt one. Being fail-open made a half-correct record worse
// than a broken one.
//
// The count is asserted because the drop alone is invisible, and an
// invisible drop is the same defect wearing a different hat: the flow that
// wrote the bad marker is still wrong, and its declared-quiet interval is
// silently gone.
func TestReadGroupRecordsDropsAndCountsAMarkerWithNoTimestamp(t *testing.T) {
	p, err := Create(RunsRoot(t.TempDir()), "web", "checkout", "r1")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := AppendGroupRecord(p, GroupRecord{Phase: "start", Name: "real", TS: ts("2026-08-21T10:00:00Z")}); err != nil {
		t.Fatalf("AppendGroupRecord: %v", err)
	}
	// Written raw, because AppendGroupRecord now refuses to produce it —
	// which is precisely why the read seam still needs its own guard.
	if err := appendRaw(p.RunDir, "{\"phase\":\"start\",\"name\":\"warmup\",\"quiet\":true}\n"); err != nil {
		t.Fatalf("appendRaw: %v", err)
	}

	got, skipped, err := ReadGroupRecords(p)
	if err != nil {
		t.Fatalf("ReadGroupRecords: %v", err)
	}
	if len(got) != 1 || got[0].Name != "real" {
		t.Fatalf("records = %+v, want only the timestamped one", got)
	}
	if skipped != 1 {
		t.Fatalf("skipped = %d, want 1 — a `ts`-less line is valid JSON that is not a record, and the caller must be able to say so", skipped)
	}
}

// TestDeriveGroupsIgnoresAMarkerWithNoTimestamp pins the third seam — the
// one records assembled in memory reach without passing either of the
// others — and, in the same fixture, that the surviving records still
// derive correctly. Dropping the bad record at the cost of the good ones
// would trade this defect for a quieter one.
func TestDeriveGroupsIgnoresAMarkerWithNoTimestamp(t *testing.T) {
	records := []GroupRecord{
		{Phase: "start", Name: "warmup", Quiet: true}, // no TS
		{Phase: "start", Name: "browse", TS: ts("2026-08-21T10:00:00Z")},
		{Phase: "start", Name: "checkout", TS: ts("2026-08-21T10:00:10Z")},
	}
	got := DeriveGroups(records, ts("2026-08-21T10:00:20Z"))

	for _, g := range got {
		if g.Name == "warmup" {
			t.Fatalf("a marker with no ts opened a group: %+v", g)
		}
		if g.StartedAt.IsZero() {
			t.Fatalf("a group starting at the zero time reached the manifest: %+v — with Quiet it covers all of history and capture.FindGaps subtracts it from every gap in the run", g)
		}
	}
	if len(got) != 2 {
		t.Fatalf("groups = %+v, want the two real ones — rejecting the bad record must not cost the good ones", got)
	}
	if got[0].Name != "browse" || !got[0].StartedAt.Equal(ts("2026-08-21T10:00:00Z")) {
		t.Fatalf("groups[0] = %+v, want browse at 10:00:00", got[0])
	}
	if got[1].Name != "checkout" || !got[1].EndedAt.Equal(ts("2026-08-21T10:00:20Z")) {
		t.Fatalf("groups[1] = %+v, want checkout closing at finishedAt", got[1])
	}
}
