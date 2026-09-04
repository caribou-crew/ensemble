package pairs

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/caribou-crew/ensemble/retrace/diff"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

func TestDirForNamesADirectorySafeSiblingOfBsDirectory(t *testing.T) {
	a := diff.RunRef{Kind: "run", RunID: "20260904T120000Z-abc1234", Manifest: runs.Manifest{App: "web"}}
	got := DirFor("/tmp/mobile-run", a)
	want := filepath.Join("/tmp/mobile-run", "diffs", "web__20260904T120000Z-abc1234")
	if got != want {
		t.Errorf("DirFor = %q, want %q", got, want)
	}
}

func TestDirForUsesTheLiteralReferenceTokenForABundleSide(t *testing.T) {
	a := diff.RunRef{Kind: "bundle", RunID: "20260801T000000Z-oldoldo", Manifest: runs.Manifest{App: "web"}}
	got := DirFor("/tmp/mobile-run", a)
	want := filepath.Join("/tmp/mobile-run", "diffs", "web__reference")
	if got != want {
		t.Errorf("DirFor = %q, want %q — a bundle side must key on the stable \"reference\" token, not the run id currently backing it", got, want)
	}
}

func TestIDSanitizesUnsafeRunes(t *testing.T) {
	a := diff.RunRef{Kind: "run", RunID: "weird/id@thing", Manifest: runs.Manifest{App: "my app"}}
	got := ID("my app", a)
	if got != "my_app__weird_id_thing" {
		t.Errorf("ID = %q, want my_app__weird_id_thing", got)
	}
}

func TestPersistWritesPairJSONAndSummaryJSONIntoDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "diffs", "web__reference")
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	s := diff.Summary{
		Schema: diff.SummarySchema, Flow: "wallet-home", Verdict: "changed",
		A: diff.RunRef{Kind: "bundle", RunID: "aaa1111", Manifest: runs.Manifest{App: "web"}},
		B: diff.RunRef{Kind: "run", RunID: "bbb2222", Manifest: runs.Manifest{App: "mobile"}},
		Counts: diff.Counts{Checkpoints: 3, PixelChanged: 3, WireChanged: 1},
	}

	p, err := Persist(dir, s, now)
	if err != nil {
		t.Fatalf("Persist: %v", err)
	}
	if p.AppA != "web" || p.AppB != "mobile" {
		t.Errorf("Persist apps = %q/%q, want web/mobile", p.AppA, p.AppB)
	}
	if p.RunA != "reference" || p.RunB != "bbb2222" {
		t.Errorf("Persist runs = %q/%q, want reference/bbb2222", p.RunA, p.RunB)
	}
	if p.Verdict != "changed" || p.Counts.WireChanged != 1 {
		t.Errorf("Persist verdict/counts = %q/%+v, not copied from the Summary", p.Verdict, p.Counts)
	}
	if p.Dir != dir {
		t.Errorf("Persist Dir = %q, want %q", p.Dir, dir)
	}
	if _, err := os.Stat(filepath.Join(dir, "pair.json")); err != nil {
		t.Errorf("pair.json not written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "summary.json")); err != nil {
		t.Errorf("summary.json not written: %v", err)
	}

	read, err := Read(dir)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if read.AppA != "web" || read.AppB != "mobile" || read.Dir != dir {
		t.Errorf("Read = %+v, want it to round-trip what Persist wrote", read)
	}

	sum, err := ReadSummary(dir)
	if err != nil {
		t.Fatalf("ReadSummary: %v", err)
	}
	if sum.Verdict != "changed" || sum.A.Manifest.App != "web" || sum.B.Manifest.App != "mobile" {
		t.Errorf("ReadSummary = %+v, want it to round-trip the persisted Summary", sum)
	}
}

func TestListWalksEveryRunsDiffsSubfolderNewestFirstAndSkipsAMalformedOne(t *testing.T) {
	root := t.TempDir()
	runsRoot := runs.RunsRoot(root)
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)

	p1, err := runs.Create(runsRoot, "mobile", "checkout", "20260904T120000Z-aaa1111")
	if err != nil {
		t.Fatalf("runs.Create: %v", err)
	}
	sWeb := diff.Summary{Verdict: "changed",
		A: diff.RunRef{Kind: "bundle", Manifest: runs.Manifest{App: "web"}},
		B: diff.RunRef{Kind: "run", RunID: "20260904T120000Z-aaa1111", Manifest: runs.Manifest{App: "mobile"}}}
	if _, err := Persist(DirFor(p1.RunDir, sWeb.A), sWeb, now); err != nil {
		t.Fatalf("Persist web: %v", err)
	}
	sDesktop := sWeb
	sDesktop.A = diff.RunRef{Kind: "run", RunID: "zzz9999", Manifest: runs.Manifest{App: "desktop"}}
	if _, err := Persist(DirFor(p1.RunDir, sDesktop.A), sDesktop, now.Add(time.Minute)); err != nil {
		t.Fatalf("Persist desktop: %v", err)
	}

	// A malformed pair.json in its own diffs subfolder — must be skipped,
	// not fail the whole listing.
	badDir := filepath.Join(p1.RunDir, "diffs", "broken__entry")
	if err := os.MkdirAll(badDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(badDir, "pair.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A run with no diffs/ subfolder at all — the common case.
	if _, err := runs.Create(runsRoot, "mobile", "checkout", "20260904T130000Z-bbb2222"); err != nil {
		t.Fatalf("runs.Create: %v", err)
	}

	got, err := List(runsRoot)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("List returned %d pairs, want 2 (the malformed one skipped): %+v", len(got), got)
	}
	if got[0].AppA != "desktop" || got[1].AppA != "web" {
		t.Errorf("List order = %q, %q, want desktop then web (newest computedAt first)", got[0].AppA, got[1].AppA)
	}
}

func TestListOnARootWithNoRunsReturnsEmptyNotNil(t *testing.T) {
	got, err := List(runs.RunsRoot(t.TempDir()))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got == nil {
		t.Error("List returned nil, want an empty (non-nil) slice — this marshals to null, not []")
	}
}
