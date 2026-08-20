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
	dir := t.TempDir()
	if err := AppendGroupRecord(dir, GroupRecord{Phase: "start", Name: "a", TS: ts("2026-08-21T10:00:00Z")}); err != nil {
		t.Fatalf("AppendGroupRecord: %v", err)
	}
	if err := appendRaw(dir, "{not json\n"); err != nil {
		t.Fatalf("appendRaw: %v", err)
	}
	if err := AppendGroupRecord(dir, GroupRecord{Phase: "end", TS: ts("2026-08-21T10:00:05Z")}); err != nil {
		t.Fatalf("AppendGroupRecord: %v", err)
	}
	got, err := ReadGroupRecords(dir)
	if err != nil {
		t.Fatalf("ReadGroupRecords: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("a corrupt marker line must be dropped, not fatal: %+v", got)
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
