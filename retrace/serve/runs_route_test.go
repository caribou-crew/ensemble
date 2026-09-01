package serve

import (
	"image/color"
	"testing"

	"github.com/caribou-crew/ensemble/core/trace"
)

// RunsFor is the runs-list drill-down: every run of one surface, newest
// first, each with the verdict its own comparison produced. The queue shows
// one row per surface (the newest run); this is what a reviewer drills into
// to see its history.
func TestRunsForListsEveryRunNewestFirst(t *testing.T) {
	cwd := t.TempDir()

	// One reference, then a later differing run. recordRun stamps ascending
	// run ids, so newest-first is a real ordering to assert.
	recordRun(t, cwd, "web", "cart", runA, map[string][]byte{"cart": shotPNG(t, white)},
		[]trace.Hop{hop(1, "GET", "/cart", 200, `{"n":1}`)})
	acceptRef(t, cwd, "web", "cart", runA)
	recordRun(t, cwd, "web", "cart", runB, map[string][]byte{"cart": shotPNG(t, color.RGBA{0, 0, 255, 255})},
		[]trace.Hop{hop(1, "GET", "/cart", 200, `{"n":1}`)})

	rows, err := RunsFor(deps(t, cwd), "web", "cart")
	if err != nil {
		t.Fatalf("RunsFor: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 runs, got %d: %+v", len(rows), rows)
	}
	// Newest first: runB was recorded after runA.
	if rows[0].RunID != runB || rows[1].RunID != runA {
		t.Fatalf("runs not newest-first: %s then %s", rows[0].RunID, rows[1].RunID)
	}
	// The newer run differs from the reference on every pixel -> changed;
	// the reference-run compares against itself -> pass.
	if rows[0].Verdict != "changed" {
		t.Errorf("newest run verdict = %q, want changed", rows[0].Verdict)
	}
	if rows[1].Verdict != "pass" {
		t.Errorf("reference run verdict = %q, want pass", rows[1].Verdict)
	}
	// Locally recorded fixtures carry no source.json -> Source stays nil.
	for _, r := range rows {
		if r.Source != nil {
			t.Errorf("run %s reported a CI source for a locally recorded fixture", r.RunID)
		}
		if r.Gates == nil {
			t.Errorf("run %s has nil Gates — must be [] on the wire", r.RunID)
		}
	}
}

func TestRunsForIsEmptyForASurfaceWithNoRuns(t *testing.T) {
	cwd := t.TempDir()
	rows, err := RunsFor(deps(t, cwd), "web", "never-recorded")
	if err != nil {
		t.Fatalf("RunsFor: %v", err)
	}
	if rows == nil {
		t.Fatalf("RunsFor returned nil, want an empty non-nil slice")
	}
	if len(rows) != 0 {
		t.Fatalf("expected no runs, got %d", len(rows))
	}
}
