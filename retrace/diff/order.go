package diff

// Section groups paired entries by flow part (runs.Group name), so a
// review UI can render "browse", "checkout", … as separate blocks instead
// of one flat list. Counts tallies, for each class in Classes, how many
// entries in this section carry it.
type Section struct {
	Name    string         `json:"name"`
	Entries []Entry        `json:"entries"`
	Counts  map[string]int `json:"counts"`
}

// LISIndices returns the indices (into seq) of one longest strictly
// increasing subsequence of seq, computed by patience sort in O(n log n).
// When several subsequences tie for longest, the one built by this
// algorithm is deterministic (leftmost tail replacement) but not
// necessarily unique among ties — callers that need "the minimal moved
// set" rely on seq being a permutation (all distinct), where ties in
// length don't translate into a choice of WHICH elements are the LIS.
func LISIndices(seq []int) []int {
	n := len(seq)
	if n == 0 {
		return nil
	}
	// tails[k] is the index into seq of the smallest possible tail value of
	// any increasing subsequence of length k+1 found so far.
	var tails []int
	prev := make([]int, n)
	for i, v := range seq {
		lo, hi := 0, len(tails)
		for lo < hi {
			mid := (lo + hi) / 2
			if seq[tails[mid]] < v {
				lo = mid + 1
			} else {
				hi = mid
			}
		}
		if lo > 0 {
			prev[i] = tails[lo-1]
		} else {
			prev[i] = -1
		}
		if lo == len(tails) {
			tails = append(tails, i)
		} else {
			tails[lo] = i
		}
	}
	out := make([]int, len(tails))
	k := tails[len(tails)-1]
	for i := len(tails) - 1; i >= 0; i-- {
		out[i] = k
		k = prev[k]
	}
	return out
}

// annotate fills in each entry's per-side ordinal position and reorder
// flag. PosA/PosB are dense 0-based ranks among the paired entries by
// SeqA/SeqB respectively — using rank rather than the raw hop Seq matters
// because Seq has gaps wherever a call went unpaired (missing/extra), and
// LIS needs a gap-free permutation to read positions from.
//
// Moved is true for any entry NOT in the longest increasing subsequence of
// "PosB read in PosA order" — the minimal set of entries that, if left in
// place, would explain every other entry's relative order staying intact.
// That is deliberately not "every entry whose position changed": e.g. A:
// [1,2,3] → B: [3,1,2] moves only "3" (see TestLISPicksTheMinimalMovedSet),
// not all three, even though every entry's absolute position changed.
func annotate(entries []Entry) []Entry {
	n := len(entries)
	if n == 0 {
		return entries
	}
	byA := argsortUint(entries, func(e Entry) uint64 { return e.SeqA })
	byB := argsortUint(entries, func(e Entry) uint64 { return e.SeqB })

	posA := make([]int, n)
	posB := make([]int, n)
	for rank, idx := range byA {
		posA[idx] = rank
	}
	for rank, idx := range byB {
		posB[idx] = rank
	}

	// seq[rank] = the PosB of the entry that sits at PosA==rank — i.e.
	// PosB values read in PosA order.
	seq := make([]int, n)
	for rank, idx := range byA {
		seq[rank] = posB[idx]
	}
	lis := LISIndices(seq)
	inLIS := make([]bool, n)
	for _, rank := range lis {
		inLIS[byA[rank]] = true
	}

	out := make([]Entry, n)
	for i, e := range entries {
		e.PosA = posA[i]
		e.PosB = posB[i]
		e.Moved = !inLIS[i]
		e.Classes = classify(e)
		out[i] = e
	}
	return out
}

func argsortUint(entries []Entry, key func(Entry) uint64) []int {
	idx := make([]int, len(entries))
	for i := range idx {
		idx[i] = i
	}
	// Insertion sort: n is the count of PAIRED calls in one diff run, never
	// large enough to need better than O(n^2) here, and stability keeps
	// output deterministic for (the practically impossible) duplicate keys.
	for i := 1; i < len(idx); i++ {
		j := i
		for j > 0 && key(entries[idx[j-1]]) > key(entries[idx[j]]) {
			idx[j-1], idx[j] = idx[j], idx[j-1]
			j--
		}
	}
	return idx
}

// classify derives an entry's Classes from what actually changed.
// BodyTolerated/BodyIgnored alone do not make an entry "changed" — those
// are differences a rule already explained; only an unexplained difference
// (BodyDiff, BodyViolations, a non-tolerated HeaderDiff, OrderingChanges,
// StatusChange) does. Reordering itself is treated as a body-level change
// on top of whatever moved/identical status the entry's OWN position gets.
//
// F1: Truncated folds into changed unconditionally. An entry the engine
// admits it could not fully compare must never come out "identical" —
// "identical" is a stronger, more reassuring claim than an empty class
// list, and Task 10's verdict logic trusts it. F6: a HeaderDiff only counts
// as changed when its Type isn't "tolerated" — mirroring BodyTolerated's
// exclusion above, so a header rule that correctly excused a change does
// not move the entry to "changed", asymmetric with body handling before
// this fix. "added"/"removed" header entries are always non-tolerated
// (Classify is always Changed for a one-sided header), so they still count.
func classify(e Entry) []string {
	headerChanged := false
	for _, hd := range e.HeaderDiff {
		if hd.Type != "tolerated" {
			headerChanged = true
			break
		}
	}
	changed := e.Truncated || len(e.BodyDiff) > 0 || len(e.BodyViolations) > 0 || headerChanged ||
		len(e.OrderingChanges) > 0 || e.StatusChange != nil
	var classes []string
	if changed {
		classes = append(classes, "changed")
	}
	if e.Moved {
		classes = append(classes, "moved")
	}
	if !changed && !e.Moved {
		classes = append(classes, "identical")
	}
	return classes
}

// unionNames merges two first-seen-order name lists: every name in a, in
// a's order, then any name that only appears in b, in b's order.
func unionNames(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, n := range a {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	for _, n := range b {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

// BuildSections buckets entries by flow part. With no declared groups on
// either side, every entry renders as one flat, unnamed section. With
// declared groups, every declared name is seeded as its own section EVEN
// WHEN it has zero entries — an empty section is the visible symptom of a
// marker placed after the traffic it meant to bracket, and silently
// omitting empty groups would hide exactly that. An entry's section is
// GroupA if set, else GroupB (an entry straddling a rename between runs
// still lands somewhere); any entry landing in neither declared name
// (GroupA and GroupB both "") is appended as a final unnamed section.
func BuildSections(entries []Entry, groups *GroupNames) []Section {
	var names []string
	if groups != nil {
		names = unionNames(groups.A, groups.B)
	}
	if len(names) == 0 {
		return []Section{buildSection("", entries)}
	}

	byName := map[string][]Entry{}
	for _, e := range entries {
		name := e.GroupA
		if name == "" {
			name = e.GroupB
		}
		byName[name] = append(byName[name], e)
	}

	out := make([]Section, 0, len(names)+1)
	for _, n := range names {
		out = append(out, buildSection(n, byName[n]))
		delete(byName, n)
	}
	if rest, ok := byName[""]; ok && len(rest) > 0 {
		out = append(out, buildSection("", rest))
	}
	return out
}

// buildSection copies its entries into a fresh backing array unconditionally
// — on the no-groups path just as much as the declared-groups path — so a
// Section's Entries never alias Wire.Paired's backing array. Before this,
// the no-groups path (BuildSections' `return []Section{buildSection("",
// entries)}`) passed the caller's slice straight through, so
// Sections[i].Entries[j] and Wire.Paired[k] shared memory on an ungrouped
// run and only diverged on a grouped one (where the byName map is built by
// appending copied values). That made the aliasing config-dependent: a
// write through Sections would mutate Wire.Paired on some runs and not
// others, which reproduces only under a particular config and looks like
// haunted data once Task 13 adds per-section review state. Copying always
// removes the dependency on BuildSections' own implementation shape.
func buildSection(name string, entries []Entry) Section {
	out := make([]Entry, len(entries))
	copy(out, entries)
	counts := map[string]int{}
	for _, e := range out {
		for _, c := range e.Classes {
			counts[c]++
		}
	}
	return Section{Name: name, Entries: out, Counts: counts}
}
