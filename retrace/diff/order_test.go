package diff

import (
	"testing"

	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

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

func TestAnnotateAssignsPosAAndPosBFromTheCorrectSide(t *testing.T) {
	// O3: annotate swapping the PosA/PosB assignment. A-order and B-order
	// deliberately differ for every entry, so a swap is observable on all
	// three, not just the one that ends up Moved.
	entries := []Entry{
		{Method: "GET", NormalizedPath: "/e1", SeqA: 1, SeqB: 30},
		{Method: "GET", NormalizedPath: "/e2", SeqA: 2, SeqB: 10},
		{Method: "GET", NormalizedPath: "/e3", SeqA: 3, SeqB: 20},
	}
	out := annotate(entries)
	byPath := map[string]Entry{}
	for _, e := range out {
		byPath[e.NormalizedPath] = e
	}
	want := map[string][2]int{
		"/e1": {0, 2},
		"/e2": {1, 0},
		"/e3": {2, 1},
	}
	for name, w := range want {
		e := byPath[name]
		if e.PosA != w[0] || e.PosB != w[1] {
			t.Fatalf("%s: PosA=%d PosB=%d, want %d,%d", name, e.PosA, e.PosB, w[0], w[1])
		}
	}
}

func TestArgsortUintSortsAscending(t *testing.T) {
	// O14: argsortUint's sort direction reversed.
	entries := []Entry{{SeqA: 30}, {SeqA: 10}, {SeqA: 20}}
	idx := argsortUint(entries, func(e Entry) uint64 { return e.SeqA })
	want := []int{1, 2, 0} // SeqA 10,20,30 -> original indices 1,2,0
	if len(idx) != 3 || idx[0] != want[0] || idx[1] != want[1] || idx[2] != want[2] {
		t.Fatalf("argsortUint = %v, want %v", idx, want)
	}
}

func TestClassifyCountsEachChangeSignalIndependently(t *testing.T) {
	// F4/O6-O9: classify reads five signals (BodyDiff, BodyViolations,
	// HeaderDiff, OrderingChanges, StatusChange) plus Truncated (F1) — each
	// alone must drive "changed", not just BodyDiff/BodyTolerated as the
	// pre-existing TestClassifyDistinguishesChangedMovedAndIdentical covered.
	cases := []struct {
		name string
		e    Entry
	}{
		{"BodyViolations", Entry{BodyViolations: []FieldDiff{{Path: "x"}}}},
		{"HeaderDiff", Entry{HeaderDiff: []HeaderDiff{{Type: "changed"}}}},
		{"OrderingChanges", Entry{OrderingChanges: []FieldDiff{{Path: "x"}}}},
		{"StatusChange", Entry{StatusChange: &StatusChange{A: 200, B: 500}}},
		{"Truncated", Entry{Truncated: true}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := classify(c.e)
			if len(got) != 1 || got[0] != "changed" {
				t.Fatalf("classify(%s) = %v, want [changed]", c.name, got)
			}
		})
	}
}

func TestClassifyDoesNotCountAToleratedHeaderAsChanged(t *testing.T) {
	// F6: mirrors BodyTolerated's exclusion — a header rule that correctly
	// excused a change must not move the entry to "changed", symmetric with
	// body handling.
	got := classify(Entry{HeaderDiff: []HeaderDiff{{Type: "tolerated"}}})
	if len(got) != 1 || got[0] != "identical" {
		t.Fatalf("classify(tolerated header only) = %v, want [identical]", got)
	}
}

func TestClassifyCountsAViolatingHeaderAsChanged(t *testing.T) {
	got := classify(Entry{HeaderDiff: []HeaderDiff{{Type: "violation"}}})
	if len(got) != 1 || got[0] != "changed" {
		t.Fatalf("classify(violation header) = %v, want [changed]", got)
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

func TestUnionNamesIncludesNamesOnlyOnB(t *testing.T) {
	// O12: unionNames returning only A's names.
	got := unionNames([]string{"browse"}, []string{"browse", "checkout"})
	want := []string{"browse", "checkout"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("unionNames = %v, want %v", got, want)
	}
}

func TestBuildSectionsFallsBackToGroupBWhenGroupAIsEmpty(t *testing.T) {
	// O11: dropping BuildSections' GroupB fallback.
	entries := []Entry{
		{Method: "GET", NormalizedPath: "/x", GroupA: "", GroupB: "checkout", Classes: []string{"identical"}},
	}
	groups := &GroupNames{A: []string{"browse"}, B: []string{"browse", "checkout"}}
	sections := BuildSections(entries, groups)
	byName := map[string]Section{}
	for _, s := range sections {
		byName[s.Name] = s
	}
	if len(byName["checkout"].Entries) != 1 {
		t.Fatalf("checkout section = %+v, want the entry to fall back to GroupB", byName["checkout"])
	}
}

func TestBuildSectionsAppendsATrailingUnnamedSectionForUngroupedEntries(t *testing.T) {
	// O15: dropping the trailing unnamed section.
	entries := []Entry{
		{Method: "GET", NormalizedPath: "/browse", GroupA: "browse", GroupB: "browse", Classes: []string{"identical"}},
		{Method: "GET", NormalizedPath: "/orphan", GroupA: "", GroupB: "", Classes: []string{"identical"}},
	}
	groups := &GroupNames{A: []string{"browse"}, B: []string{"browse"}}
	sections := BuildSections(entries, groups)
	if len(sections) != 2 {
		t.Fatalf("len(sections) = %d, want 2 (browse + trailing unnamed)", len(sections))
	}
	if sections[1].Name != "" || len(sections[1].Entries) != 1 {
		t.Fatalf("sections[1] = %+v, want a trailing unnamed section with the orphan entry", sections[1])
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

// TestUngroupedSectionsDoNotAliasWirePaired pins the ruling in buildSection
// itself, not the round-3 combination that happened to depend on it.
// Round 3 pinned "aliasing restored + the Sections loop dropped" — a
// two-mechanism fixture that only fires when BOTH change together. This
// fixture isolates the one mechanism: it goes through Build on an
// UNGROUPED run (the path that used to alias Wire.Paired's backing array
// straight through, per order.go's buildSection doc), writes through
// Sections[0].Entries[0], and asserts Wire.Paired[0] did not move. Nothing
// else about the Sections loop is touched, so this fails if and only if
// buildSection stops copying on the no-groups path — exactly the ruling
// round 4 found unpinned. Reverting buildSection's `out := make + copy`
// back to `out := entries` must turn this red on its own.
func TestUngroupedSectionsDoNotAliasWirePaired(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	h := hop(1, "GET", "/cart", 200, "", `{"ok":true}`)
	writeWireFile(t, dirA, []trace.Hop{h})
	writeWireFile(t, dirB, []trace.Hop{h})
	a := RunRef{Kind: "run", Dir: dirA, Manifest: manifest("a", nil, nil, okCapture())}
	b := RunRef{Kind: "run", Dir: dirB, Manifest: manifest("b", nil, nil, okCapture())}
	cfg := baseConfig(t)

	s := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: a, B: b, Cfg: cfg})
	if len(s.Wire.Paired) == 0 || len(s.Sections) == 0 || len(s.Sections[0].Entries) == 0 {
		t.Fatalf("test setup: nothing to mutate through; Wire.Paired=%+v Sections=%+v", s.Wire.Paired, s.Sections)
	}
	original := s.Wire.Paired[0].Method

	s.Sections[0].Entries[0].Method = "MUTATED-VIA-SECTIONS"

	if s.Wire.Paired[0].Method != original {
		t.Fatalf("Wire.Paired[0].Method = %q after writing through Sections[0].Entries[0], want unchanged %q — an ungrouped Build must not let Sections and Wire.Paired share backing memory",
			s.Wire.Paired[0].Method, original)
	}
}

// TestGroupedSectionsDoNotAliasWirePaired is the grouped-path companion:
// the brief that produced this fix asked whether the grouped path deserves
// the same assertion, since it copies today via the byName map's append
// but nothing pins that either. Pinning the property Build promises here
// — not BuildSections' current implementation of it — means a future
// refactor of the grouped collection path (e.g. one that started aliasing
// byName's slices) would be caught here too, not only on the ungrouped
// fixture above.
//
// IMPORTANT: this test does NOT kill the buildSection mutation that
// TestUngroupedSectionsDoNotAliasWirePaired kills (reverting buildSection's
// `out := make + copy` back to `out := entries`). Measured directly: with
// that mutation applied, this test still PASSES, because byName[n] is
// already a freshly appended slice before buildSection ever sees it —
// buildSection's own copy is redundant on this path today. Its green run
// is evidence the grouped path still doesn't alias, NOT evidence that the
// buildSection ruling under test in the other function was exercised.
func TestGroupedSectionsDoNotAliasWirePaired(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	h := hop(1, "GET", "/cart", 200, "", `{"ok":true}`)
	writeWireFile(t, dirA, []trace.Hop{h})
	writeWireFile(t, dirB, []trace.Hop{h})
	groups := []runs.Group{{Name: "checkout"}}
	a := RunRef{Kind: "run", Dir: dirA, Manifest: manifest("a", nil, groups, okCapture())}
	b := RunRef{Kind: "run", Dir: dirB, Manifest: manifest("b", nil, groups, okCapture())}
	cfg := baseConfig(t)
	opts, err := OptionsFor(cfg, a.Manifest, b.Manifest)
	if err != nil {
		t.Fatalf("OptionsFor: %v", err)
	}

	s := mustBuild(t, BuildInput{App: "app", Flow: "flow", A: a, B: b, Cfg: cfg, Options: opts})
	if len(s.Wire.Paired) == 0 || len(s.Sections) == 0 || len(s.Sections[len(s.Sections)-1].Entries) == 0 {
		t.Fatalf("test setup: nothing to mutate through; Wire.Paired=%+v Sections=%+v", s.Wire.Paired, s.Sections)
	}
	last := len(s.Sections) - 1
	original := s.Wire.Paired[0].Method

	s.Sections[last].Entries[0].Method = "MUTATED-VIA-SECTIONS"

	if s.Wire.Paired[0].Method != original {
		t.Fatalf("Wire.Paired[0].Method = %q after writing through Sections[%d].Entries[0], want unchanged %q — a grouped Build must not let Sections and Wire.Paired share backing memory",
			s.Wire.Paired[0].Method, last, original)
	}
}
