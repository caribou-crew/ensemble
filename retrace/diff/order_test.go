package diff

import "testing"

func TestLISPicksTheMinimalMovedSet(t *testing.T) {
	// A: 1 2 3 → B: 3 1 2 is ONE call moving (item "3", from last to
	// first), not three, even though every item's absolute position
	// changed. seq is "PosB read in PosA order": item 1 is 2nd in B (index
	// 1), item 2 is 3rd in B (index 2), item 3 is 1st in B (index 0).
	seq := []int{1, 2, 0}
	lis := LISIndices(seq)
	if len(lis) != 2 {
		t.Fatalf("LISIndices(%v) = %v, want a length-2 subsequence", seq, lis)
	}
	inLIS := map[int]bool{}
	for _, i := range lis {
		inLIS[i] = true
	}
	if !inLIS[0] || !inLIS[1] {
		t.Fatalf("LISIndices(%v) = %v, want indices {0,1} (items 1,2) in the LIS", seq, lis)
	}
	if inLIS[2] {
		t.Fatalf("LISIndices(%v) = %v, index 2 (item 3) must be the one moved element, not in the LIS", seq, lis)
	}
}

func TestAnnotateMarksOnlyTheMinimalMovedSet(t *testing.T) {
	// End-to-end through annotate: three paired entries whose A-order is
	// 1,2,3 (by SeqA) and whose B-order is 3,1,2 (by SeqB). Only the entry
	// that was call "3" should come out Moved.
	entries := []Entry{
		{Method: "GET", NormalizedPath: "/a", SeqA: 1, SeqB: 20},
		{Method: "GET", NormalizedPath: "/b", SeqA: 2, SeqB: 30},
		{Method: "GET", NormalizedPath: "/c", SeqA: 3, SeqB: 10},
	}
	out := annotate(entries)
	moved := map[string]bool{}
	for _, e := range out {
		moved[e.NormalizedPath] = e.Moved
	}
	if moved["/a"] || moved["/b"] {
		t.Fatalf("moved = %+v, want only /c moved", moved)
	}
	if !moved["/c"] {
		t.Fatalf("moved = %+v, want /c moved (it went from last in A to first in B)", moved)
	}
}

func TestClassifyDistinguishesChangedMovedAndIdentical(t *testing.T) {
	changed := classify(Entry{BodyDiff: []FieldDiff{{Path: "x"}}})
	if len(changed) != 1 || changed[0] != "changed" {
		t.Fatalf("classify(changed) = %v", changed)
	}
	tolerated := classify(Entry{BodyTolerated: []FieldDiff{{Path: "x"}}})
	if len(tolerated) != 1 || tolerated[0] != "identical" {
		t.Fatalf("classify(tolerated-only) = %v, want [identical] — a tolerated diff alone must not count as changed", tolerated)
	}
	moved := classify(Entry{Moved: true})
	if len(moved) != 1 || moved[0] != "moved" {
		t.Fatalf("classify(moved, no other change) = %v, want exactly [moved]", moved)
	}
	both := classify(Entry{Moved: true, BodyDiff: []FieldDiff{{Path: "x"}}})
	if len(both) != 2 || both[0] != "changed" || both[1] != "moved" {
		t.Fatalf("classify(moved+changed) = %v, want [changed moved]", both)
	}
}

func TestSectionsSeedDeclaredButEmptyParts(t *testing.T) {
	// A marker declares "checkout" but nothing ever landed there — that
	// empty section is the exact symptom of a marker placed after the
	// traffic it meant to bracket, so it must still appear, not be
	// silently dropped.
	entries := []Entry{
		{Method: "GET", NormalizedPath: "/browse", GroupA: "browse", GroupB: "browse", Classes: []string{"identical"}},
	}
	groups := &GroupNames{A: []string{"browse", "checkout"}, B: []string{"browse", "checkout"}}
	sections := BuildSections(entries, groups)
	if len(sections) != 2 {
		t.Fatalf("len(sections) = %d, want 2 (browse, checkout)", len(sections))
	}
	byName := map[string]Section{}
	for _, s := range sections {
		byName[s.Name] = s
	}
	checkout, ok := byName["checkout"]
	if !ok {
		t.Fatalf("sections = %+v, want a seeded checkout section", sections)
	}
	if len(checkout.Entries) != 0 {
		t.Fatalf("checkout.Entries = %+v, want empty", checkout.Entries)
	}
	if len(byName["browse"].Entries) != 1 {
		t.Fatalf("browse.Entries = %+v, want 1", byName["browse"].Entries)
	}
}

func TestUngroupedRunsRenderAsOneFlatSection(t *testing.T) {
	entries := []Entry{
		{Method: "GET", NormalizedPath: "/a", Classes: []string{"identical"}},
		{Method: "GET", NormalizedPath: "/b", Classes: []string{"changed"}},
	}
	sections := BuildSections(entries, nil)
	if len(sections) != 1 {
		t.Fatalf("len(sections) = %d, want 1", len(sections))
	}
	if len(sections[0].Entries) != 2 {
		t.Fatalf("sections[0].Entries = %+v, want both entries", sections[0].Entries)
	}
	if sections[0].Counts["identical"] != 1 || sections[0].Counts["changed"] != 1 {
		t.Fatalf("sections[0].Counts = %+v, want identical:1 changed:1", sections[0].Counts)
	}

	// Same when GroupNames is non-nil but both sides declared nothing.
	sections2 := BuildSections(entries, &GroupNames{})
	if len(sections2) != 1 || len(sections2[0].Entries) != 2 {
		t.Fatalf("BuildSections with empty GroupNames = %+v, want one flat section", sections2)
	}
}
