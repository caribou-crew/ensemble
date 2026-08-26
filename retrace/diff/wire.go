// Package diff compares two retrace runs across the wire (this file) and
// pixel (pixel/) planes. wire.go pairs requests between run A and run B by
// similarity, diffs paired calls field by field under retrace/rules, and
// detects array reorders. order.go turns the paired result into LIS-based
// hop-level reorder detection and grouped sections.
package diff

import (
	"encoding/json"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/retrace/rules"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

// Options configures one DiffWire call. Normalize is a behaviour, not a
// config object, precisely so this package never imports retrace/config —
// nothing here needs anything else config.Config carries.
type Options struct {
	WireIgnore []string
	Rules      []rules.Rule
	Normalize  func(path string) string // config.NormalizePath
	GroupsA    []runs.Group
	GroupsB    []runs.Group
	Deviations []Deviation
}

// Pair is one call matched between run A and run B by PairCalls.
type Pair struct {
	Method         string
	NormalizedPath string
	A, B           trace.Hop
}

// CallSimilarity weights: status carries real weight because a 304 cache
// hit and a 200 with a body are not the same event — which is exactly the
// pair a positional zip invents.
func CallSimilarity(a, b trace.Hop) float64 {
	s := 0.0
	if a.Status == b.Status {
		s += 0.3
	}
	return s + 0.5*bodySimilarity(a.Resp.Body, b.Resp.Body) + 0.2*bodySimilarity(a.Req.Body, b.Req.Body)
}

// bodySimilarity scores two raw body strings from 0 (nothing alike) to 1
// (identical). JSON bodies are compared on their canonical form first so
// key order never lowers the score; everything else (including bodies that
// aren't JSON) falls back to a Sørensen–Dice coefficient over character
// bigrams, which gives partial credit for "mostly the same string" without
// an O(n·m) edit-distance computation. This exact formula is not specified
// by the brief beyond CallSimilarity's own weights — a documented judgment
// call, not a port of anything.
func bodySimilarity(a, b string) float64 {
	ca, cb := canonicalBodyForSimilarity(a), canonicalBodyForSimilarity(b)
	if ca == cb {
		return 1
	}
	return diceBigram(ca, cb)
}

func canonicalBodyForSimilarity(s string) string {
	var v any
	if err := json.Unmarshal([]byte(strings.TrimSpace(s)), &v); err == nil {
		return canonicalJSON(v)
	}
	return s
}

func diceBigram(a, b string) float64 {
	if len(a) < 2 || len(b) < 2 {
		if a == b {
			return 1
		}
		return 0
	}
	return diceFromBigrams(bigramCounts(a), bigramCounts(b))
}

// bigramCounts and diceFromBigrams split diceBigram's string-based work
// (build a bigram map) from its comparison (score two maps), so a caller
// that already has bigram maps for both sides — align's per-hop
// precomputation below — can skip rebuilding them per pair.
func bigramCounts(s string) map[string]int {
	m := map[string]int{}
	for i := 0; i+2 <= len(s); i++ {
		m[s[i:i+2]]++
	}
	return m
}

func diceFromBigrams(ma, mb map[string]int) float64 {
	inter, total := 0, 0
	for k, va := range ma {
		if vb, ok := mb[k]; ok {
			if va < vb {
				inter += va
			} else {
				inter += vb
			}
		}
		total += va
	}
	for _, vb := range mb {
		total += vb
	}
	if total == 0 {
		return 1
	}
	return 2 * float64(inter) / float64(total)
}

// hopSim is a hop's precomputed similarity ingredients: the canonicalized
// req/resp bodies and their bigram maps, built once per hop instead of
// once per (i,j) pair.
//
// F12: align used to call CallSimilarity(as[i], bs[j]) once per DP cell in
// the fill loop AND AGAIN for the same (i,j) in the traceback loop — each
// call re-json.Unmarshal-ing and re-canonicalizing both bodies and
// rebuilding two bigram maps from scratch. For a 120x120 grid with 20KB
// bodies that's up to n*m*2 canonicalizations of a value with only n+m
// distinct inputs (measured at 9.5s; see TestAlignPerformanceOnAShapedInput
// for the after number). Precomputing per hop and scoring each pair
// exactly once (see pairSimilarity/the sim matrix in align) collapses the
// canonicalization work back down to O(n+m) and the scoring work to
// exactly n*m calls total instead of up to 2*n*m.
type hopSim struct {
	status                  int
	reqCanon, respCanon     string
	reqBigrams, respBigrams map[string]int
}

func prepHopSim(h trace.Hop) hopSim {
	hs := hopSim{
		status:    h.Status,
		reqCanon:  canonicalBodyForSimilarity(h.Req.Body),
		respCanon: canonicalBodyForSimilarity(h.Resp.Body),
	}
	if len(hs.reqCanon) >= 2 {
		hs.reqBigrams = bigramCounts(hs.reqCanon)
	}
	if len(hs.respCanon) >= 2 {
		hs.respBigrams = bigramCounts(hs.respCanon)
	}
	return hs
}

// pairSimilarityHook, when non-nil, is called once per pairSimilarity
// invocation. Test-only counting seam — see
// TestAlignScoresEachPairAtMostOnce — left as a no-op check in production.
var pairSimilarityHook func()

// pairSimilarity scores two hops' precomputed ingredients. It is the exact
// same formula as CallSimilarity/bodySimilarity/diceBigram (same weights,
// same canonical-form-equality and len<2 short-circuits) — see those for
// the rationale — just fed from hopSim's precomputed fields instead of
// recomputing them from raw bodies. TestPairSimilarityMatchesCallSimilarity
// pins that the two never diverge.
func pairSimilarity(a, b hopSim) float64 {
	if pairSimilarityHook != nil {
		pairSimilarityHook()
	}
	s := 0.0
	if a.status == b.status {
		s += 0.3
	}
	return s + 0.5*canonSimilarity(a.respCanon, a.respBigrams, b.respCanon, b.respBigrams) +
		0.2*canonSimilarity(a.reqCanon, a.reqBigrams, b.reqCanon, b.reqBigrams)
}

// canonSimilarity mirrors bodySimilarity's post-canonicalization logic
// (equal canonical forms score 1; otherwise fall back to the bigram dice
// coefficient, with diceBigram's own len<2 short-circuit — unequal short
// strings score 0, since the equal case is already handled above).
func canonSimilarity(ca string, ma map[string]int, cb string, mb map[string]int) float64 {
	if ca == cb {
		return 1
	}
	if len(ca) < 2 || len(cb) < 2 {
		return 0
	}
	return diceFromBigrams(ma, mb)
}

// align is Needleman-Wunsch with a zero gap score: a pair is made whenever
// it beats leaving both sides unmatched, and the BEST available match wins.
// Order-preserving, so it never pairs across a reorder — that is the
// reorder detector's job (order.go), not the aligner's.
//
// Every (i,j) pair's similarity is precomputed once into sim before the DP
// fill loop, then read (not recomputed) by both the fill loop and the
// traceback loop — see hopSim/pairSimilarity above (F12). This changes
// nothing about evaluation order: the fill and traceback loops still walk
// i/j in exactly the same directions and compare the same score[][]
// entries with the same >= tie-break as before, so which of two
// equal-scoring candidates wins is unaffected — only where the diag value
// comes from (a lookup instead of a live call) changed.
func align(as, bs []trace.Hop) (pairs [][2]trace.Hop, aOnly, bOnly []trace.Hop) {
	n, m := len(as), len(bs)
	if n == 0 || m == 0 {
		return nil, as, bs
	}
	aSim := make([]hopSim, n)
	for i, h := range as {
		aSim[i] = prepHopSim(h)
	}
	bSim := make([]hopSim, m)
	for j, h := range bs {
		bSim[j] = prepHopSim(h)
	}
	sim := make([][]float64, n)
	for i := range sim {
		sim[i] = make([]float64, m)
		for j := range sim[i] {
			sim[i][j] = pairSimilarity(aSim[i], bSim[j])
		}
	}

	score := make([][]float64, n+1)
	for i := range score {
		score[i] = make([]float64, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			diag := sim[i][j] + score[i+1][j+1]
			score[i][j] = math.Max(diag, math.Max(score[i+1][j], score[i][j+1]))
		}
	}
	i, j := 0, 0
	for i < n && j < m {
		diag := sim[i][j] + score[i+1][j+1]
		switch {
		case diag >= score[i+1][j] && diag >= score[i][j+1]:
			pairs = append(pairs, [2]trace.Hop{as[i], bs[j]})
			i, j = i+1, j+1
		case score[i+1][j] >= score[i][j+1]:
			aOnly = append(aOnly, as[i])
			i++
		default:
			bOnly = append(bOnly, bs[j])
			j++
		}
	}
	aOnly = append(aOnly, as[i:]...)
	bOnly = append(bOnly, bs[j:]...)
	return pairs, aOnly, bOnly
}

// SplitPath splits trace.Hop.Path (RequestURI(), so query included) on the
// first '?'.
func SplitPath(hopPath string) (path, rawQuery string) {
	path, rawQuery, _ = strings.Cut(hopPath, "?")
	return path, rawQuery
}

// NormalizeQuery sorts a raw query string's "k=v" pairs so that
// "?b=2&a=1" and "?a=1&b=2" produce the same bucket key. Pairs are sorted
// as opaque tokens, not decoded — pairing needs a stable canonical form,
// not a fully-parsed query.
func NormalizeQuery(rawQuery string) string {
	if rawQuery == "" {
		return ""
	}
	parts := strings.Split(rawQuery, "&")
	sort.Strings(parts)
	return strings.Join(parts, "&")
}

func bucketKey(h trace.Hop, normalize func(string) string) string {
	path, rawQuery := SplitPath(h.Path)
	return h.Method + " " + normalize(path) + "?" + NormalizeQuery(rawQuery)
}

// PairCalls buckets calls by method + normalized path + normalized query,
// then aligns each bucket independently. Bucket keys are visited in
// first-seen order (all of a's calls, in a's order, then any key that only
// appears in b, in b's order) — never map order — so the output is
// deterministic across runs of the same two inputs.
func PairCalls(a, b []trace.Hop, normalize func(string) string) (pairs []Pair, missing, extra []trace.Hop) {
	if normalize == nil {
		normalize = func(p string) string { return p }
	}
	type bucket struct{ as, bs []trace.Hop }
	buckets := map[string]*bucket{}
	var order []string
	get := func(k string) *bucket {
		bk, ok := buckets[k]
		if !ok {
			bk = &bucket{}
			buckets[k] = bk
			order = append(order, k)
		}
		return bk
	}
	for _, h := range a {
		bk := get(bucketKey(h, normalize))
		bk.as = append(bk.as, h)
	}
	for _, h := range b {
		bk := get(bucketKey(h, normalize))
		bk.bs = append(bk.bs, h)
	}
	for _, k := range order {
		bk := buckets[k]
		aligned, aOnly, bOnly := align(bk.as, bk.bs)
		for _, pr := range aligned {
			path, _ := SplitPath(pr[0].Path)
			pairs = append(pairs, Pair{
				Method:         pr[0].Method,
				NormalizedPath: normalize(path),
				A:              pr[0],
				B:              pr[1],
			})
		}
		missing = append(missing, aOnly...)
		extra = append(extra, bOnly...)
	}
	return pairs, missing, extra
}

// FieldDiff is one leaf-level difference found in a request or response
// body, or the whole-body fallback when a body couldn't be parsed as JSON.
type FieldDiff struct {
	Scope   string `json:"scope"` // "req" | "resp"
	Path    string `json:"path"`  // dotted field path; array elements are "items[0].sku"
	Type    string `json:"type"`  // "changed" | "added" | "removed"
	A       any    `json:"a"`
	B       any    `json:"b"`
	Matcher string `json:"matcher,omitempty"`
	Glob    string `json:"glob,omitempty"`
}

type HeaderDiff struct {
	Scope   string `json:"scope"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	A       string `json:"a"`
	B       string `json:"b"`
	Matcher string `json:"matcher,omitempty"`
}

type StatusChange struct {
	A int `json:"a"`
	B int `json:"b"`
}

type Entry struct {
	Method         string `json:"method"`
	NormalizedPath string `json:"normalizedPath"`
	SeqA           uint64 `json:"seqA"`
	SeqB           uint64 `json:"seqB"`
	PosA           int    `json:"posA"`
	PosB           int    `json:"posB"`
	GroupA         string `json:"groupA,omitempty"`
	GroupB         string `json:"groupB,omitempty"`
	// Moved and Truncated are NEVER omitempty (D2). Removing omitempty from
	// a bool is a WIDENING — a reader that tolerated absence still works —
	// so it cannot break a consumer, and it is what makes `entry.moved`
	// mean what a TS mirror declaring `moved: boolean` says it means.
	//
	// The absent key is runtime-safe only for as long as every consumer
	// tests truthiness: `undefined` is falsy today, and the day someone
	// writes `=== false`, a `switch`, or re-serialises the document it stops
	// being safe. "Safe as long as nobody writes ===" is a requirement
	// nobody can be told — and this task shipped a confidently WRONG note
	// about exactly this shape, which is how someone comes to write the
	// unsafe line on purpose. Every other bool on this REST surface that
	// means "did this happen" now encodes the same way.
	Moved           bool          `json:"moved"`
	Truncated       bool          `json:"truncated"`
	Classes         []string      `json:"classes"`
	StatusChange    *StatusChange `json:"statusChange,omitempty"`
	BodyDiff        []FieldDiff   `json:"bodyDiff"`
	BodyTolerated   []FieldDiff   `json:"bodyTolerated"`
	BodyViolations  []FieldDiff   `json:"bodyViolations"`
	BodyIgnored     []FieldDiff   `json:"bodyIgnored"`
	OrderingChanges []FieldDiff   `json:"orderingChanges"`
	HeaderDiff      []HeaderDiff  `json:"headerDiff"`
	// HeaderIgnored mirrors BodyIgnored one plane over: headers an `ignore`
	// matcher silenced. Separate from HeaderDiff because everything in that
	// list is a finding — classify() and triage both read it that way — and
	// an ignored header is the opposite of one.
	HeaderIgnored []HeaderDiff `json:"headerIgnored"`
}

type Wire struct {
	Paired  []Entry     `json:"paired"`
	Missing []Call      `json:"missing"`
	Extra   []Call      `json:"extra"`
	Groups  *GroupNames `json:"groups,omitempty"`
}

type Call struct {
	Method    string         `json:"method"`
	Path      string         `json:"path"`
	Seq       uint64         `json:"seq"`
	Status    int            `json:"status"`
	Group     string         `json:"group,omitempty"`
	Tolerated *ToleratedNote `json:"tolerated,omitempty"`
}

// GroupNames lists the distinct flow-part names declared on each side, in
// first-seen order. One tag per field: A and B cannot share a struct tag
// string.
type GroupNames struct {
	A []string `json:"a"`
	B []string `json:"b"`
}

// diffCtx bundles the two sources of "is this difference OK": per-call
// resolved rules.Rule matchers, and Options.WireIgnore — a flat list of
// dotted field-path globs (same glob syntax as a rule's body globs) that
// silence a field diff everywhere, without needing a per-call rule. This is
// the implementer's reading of WireIgnore — the brief gives only the
// field's type, not its semantics, and no listed test exercises it — kept
// deliberately minimal: same glob dialect as rules.BodyRule, same
// last-word-wins-over-nothing precedence (a rule match wins if both a rule
// and a WireIgnore glob apply, since rules are checked first), same
// "only reported in BodyIgnored when it actually silenced a difference"
// behaviour as a rule-level ignore.
type diffCtx struct {
	res    rules.Resolved
	ignore []string
}

func resolveField(ctx diffCtx, path string) (m rules.Matcher, glob string, matched bool) {
	for i := len(ctx.res.Body) - 1; i >= 0; i-- {
		if rules.MatchFieldGlob(ctx.res.Body[i].Glob, path) {
			return ctx.res.Body[i].Matcher, ctx.res.Body[i].Glob, true
		}
	}
	for _, g := range ctx.ignore {
		if rules.MatchFieldGlob(g, path) {
			return rules.Matcher{Kind: rules.KindIgnore}, g, true
		}
	}
	return rules.Matcher{}, "", false
}

// bodyAcc accumulates one call's body-diff findings by outcome. A FieldDiff
// is only ever appended when walk found an actual difference — never
// pre-emptively for a field a rule covers, which is what keeps BodyIgnored
// from listing fields whose values already matched (see
// TestAnIgnoreIsOnlyRecordedWhenItActuallySuppressedADifference).
type bodyAcc struct {
	Diff       []FieldDiff
	Tolerated  []FieldDiff
	Violations []FieldDiff
	Ignored    []FieldDiff
	Ordering   []FieldDiff
}

func (acc *bodyAcc) record(scope, path, typ string, a, b any, ctx diffCtx) {
	bothPresent := typ == "changed"
	m, glob, matched := resolveField(ctx, path)
	outcome := rules.Classify(m, a, b, bothPresent)
	fd := FieldDiff{Scope: scope, Path: path, Type: typ, A: a, B: b}
	if matched {
		fd.Matcher = m.Label()
		fd.Glob = glob
	}
	switch outcome {
	case rules.Ignored:
		acc.Ignored = append(acc.Ignored, fd)
	case rules.Tolerated:
		acc.Tolerated = append(acc.Tolerated, fd)
	case rules.Violation:
		acc.Violations = append(acc.Violations, fd)
	default:
		acc.Diff = append(acc.Diff, fd)
	}
}

func joinPath(parent, key string) string {
	if parent == "" {
		return key
	}
	return parent + "." + key
}

func elemPath(parent string, i int) string {
	return parent + "[" + strconv.Itoa(i) + "]"
}

// parseBody parses a payload's body as JSON. It reports (nil, false) — not
// an error — when the body is truncated (a size-capped body has no
// trustworthy tail to parse) or the body is empty or not valid JSON. The
// caller is what decides what "not both ok" means (whole-string fallback),
// per the brief: never a field tree over half-parsed data.
func parseBody(p trace.Payload) (any, bool) {
	if p.Truncated {
		return nil, false
	}
	s := strings.TrimSpace(p.Body)
	if s == "" {
		return nil, false
	}
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return nil, false
	}
	return v, true
}

func diffBodyScope(scope string, aPayload, bPayload trace.Payload, ctx diffCtx, acc *bodyAcc) {
	av, aok := parseBody(aPayload)
	bv, bok := parseBody(bPayload)
	if !aok || !bok {
		if aPayload.Body != bPayload.Body {
			acc.record(scope, "", "changed", aPayload.Body, bPayload.Body, ctx)
		}
		return
	}
	walk(scope, "", av, bv, ctx, acc)
}

// walk recurses a decoded JSON value pair, dispatching to diffObjects or
// diffArrays when both sides share that shape, and comparing scalars (or
// mismatched shapes) directly otherwise.
func walk(scope, path string, a, b any, ctx diffCtx, acc *bodyAcc) {
	if aArr, aok := a.([]any); aok {
		if bArr, bok := b.([]any); bok {
			diffArrays(scope, path, aArr, bArr, ctx, acc)
			return
		}
	}
	if aObj, aok := a.(map[string]any); aok {
		if bObj, bok := b.(map[string]any); bok {
			diffObjects(scope, path, aObj, bObj, ctx, acc)
			return
		}
	}
	if !reflect.DeepEqual(a, b) {
		acc.record(scope, path, "changed", a, b, ctx)
	}
}

func diffObjects(scope, path string, a, b map[string]any, ctx diffCtx, acc *bodyAcc) {
	keys := map[string]bool{}
	for k := range a {
		keys[k] = true
	}
	for k := range b {
		keys[k] = true
	}
	sorted := make([]string, 0, len(keys))
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	for _, k := range sorted {
		childPath := joinPath(path, k)
		av, aok := a[k]
		bv, bok := b[k]
		switch {
		case aok && bok:
			walk(scope, childPath, av, bv, ctx, acc)
		case aok:
			acc.record(scope, childPath, "removed", av, nil, ctx)
		default:
			acc.record(scope, childPath, "added", nil, bv, ctx)
		}
	}
}

// diffArrays handles a JSON array pair. Same-length arrays are checked for
// a pure reorder first: blankTolerated neutralises any field a rule would
// tolerate or ignore, and if the two sides become the same MULTISET under
// that blanking (but are not already positionally identical), the whole
// array is reported as one OrderingChanges entry instead of N field
// changes. Otherwise — different lengths, or no rule bridges what
// differs — every position is walked and any length difference beyond the
// shorter side is reported as added/removed at that index.
func diffArrays(scope, path string, a, b []any, ctx diffCtx, acc *bodyAcc) {
	n, m := len(a), len(b)
	if n == m {
		blankedA := make([]string, n)
		blankedB := make([]string, n)
		identical := true
		for i := range n {
			blankedA[i] = canonicalJSON(blankTolerated(a[i], ctx, elemPath(path, i)))
			blankedB[i] = canonicalJSON(blankTolerated(b[i], ctx, elemPath(path, i)))
			if blankedA[i] != blankedB[i] {
				identical = false
			}
		}
		if !identical && sameMultiset(blankedA, blankedB) {
			acc.Ordering = append(acc.Ordering, FieldDiff{Scope: scope, Path: path, Type: "changed", A: a, B: b})
			return
		}
		for i := range n {
			walk(scope, elemPath(path, i), a[i], b[i], ctx, acc)
		}
		return
	}
	minLen := min(n, m)
	for i := range minLen {
		walk(scope, elemPath(path, i), a[i], b[i], ctx, acc)
	}
	for i := minLen; i < n; i++ {
		acc.record(scope, elemPath(path, i), "removed", a[i], nil, ctx)
	}
	for i := minLen; i < m; i++ {
		acc.record(scope, elemPath(path, i), "added", nil, b[i], ctx)
	}
}

func sameMultiset(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	counts := map[string]int{}
	for _, s := range a {
		counts[s]++
	}
	for _, s := range b {
		counts[s]--
	}
	for _, c := range counts {
		if c != 0 {
			return false
		}
	}
	return true
}

// blankTolerated returns a copy of v with every leaf whose resolved field
// matcher EXCUSES this side's own value replaced by a fixed placeholder. It
// never compares to a second value: it is applied independently to each
// side, which is what lets diffArrays detect "same content, reordered" even
// when a tolerated field (e.g. a per-element uuid) legitimately differs in
// every element — see TestAReorderIsDetectedEvenThoughAToleratedFieldDiffersInEveryElement.
// A field with no rule (the zero/exact matcher) is never blanked, so an
// unruled differentiator correctly defeats multiset matching — see
// TestWithoutARuleTheSameReorderStillReportsPositionalChanges.
//
// F2: a value/pattern matcher only gets to blank a leaf whose OWN value it
// actually accepts — rules.Classify(m, v, v, true) != Violation, i.e. "does
// this side's value satisfy its own matcher". A side whose value VIOLATES
// its own matcher must keep its real value: that defeats the multiset
// match and pushes the element down diffArrays' positional fallback, where
// record() reports the Violation. Blanking it unconditionally (the
// pre-fix behaviour) let a genuine rule Violation vanish into a benign
// OrderingChanges entry whenever it happened to co-occur with a reorder —
// a reorder and a rule violation are independent facts and neither may
// silently swallow the other. KindIgnore is unaffected: Classify(m, v, v,
// true) always answers Ignored for it, never Violation, so an ignored
// field keeps blanking unconditionally regardless of its value.
const blankedPlaceholder = "xretrace:blanked"

func blankTolerated(v any, ctx diffCtx, path string) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = blankTolerated(val, ctx, joinPath(path, k))
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = blankTolerated(val, ctx, elemPath(path, i))
		}
		return out
	default:
		m, _, matched := resolveField(ctx, path)
		if matched && (m.Kind == rules.KindIgnore || m.Kind == rules.KindNamed || m.Kind == rules.KindPattern) &&
			rules.Classify(m, v, v, true) != rules.Violation {
			return blankedPlaceholder
		}
		return v
	}
}

// canonicalJSON renders v with object keys sorted at every level, so
// structurally identical values compare equal regardless of source key
// order. B1: encoding/json's own Marshal already sorts map keys at EVERY
// nesting level, slices included — this explicit recursive form is not
// covering a gap in the marshaller (an earlier version of this comment
// claimed one; it was wrong). It is kept anyway because it is one pass with
// no intermediate []byte allocation, marginally cheaper than round-tripping
// through json.Marshal for this package's hot path (bodySimilarity runs
// once per DP cell in align).
func canonicalJSON(v any) string {
	var b strings.Builder
	writeCanonical(&b, v)
	return b.String()
}

func writeCanonical(b *strings.Builder, v any) {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			kb, _ := json.Marshal(k)
			b.Write(kb)
			b.WriteByte(':')
			writeCanonical(b, t[k])
		}
		b.WriteByte('}')
	case []any:
		b.WriteByte('[')
		for i, e := range t {
			if i > 0 {
				b.WriteByte(',')
			}
			writeCanonical(b, e)
		}
		b.WriteByte(']')
	default:
		vb, _ := json.Marshal(t)
		b.Write(vb)
	}
}

func lowerHeaders(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[strings.ToLower(k)] = v
	}
	return out
}

// DiffHeaders compares two header maps case-insensitively. Equal values are
// omitted entirely (they're not a difference). An outcome of Ignored — from
// either a rules.Rule header matcher or, same as body fields, a value that
// simply never differed — is silenced: it never appears in the result, on
// an add/remove/change alike. Tolerated and Violation outcomes both still
// appear (never silently dropped, matching this package's "tolerated is
// still reported" rule for deviations), carrying the matcher's Label() so a
// consumer can tell "this was excused" from "this was never explained" —
// Classify never returns Tolerated for a one-sided (add/remove) header, so
// Matcher is only ever populated on a two-sided entry.
//
// F6: Type is the outcome field for a two-sided header — "changed" (no rule
// or the zero/exact matcher), "tolerated", or "violation" — mirroring
// classify's BodyTolerated exclusion so a correctly-excused header change
// does not move the entry to "changed", and surfacing a header rule
// Violation, which used to be indistinguishable from an ordinary change
// (both reported Type: "changed", with only the label-not-outcome Matcher
// field to tell them apart). A one-sided header stays "added"/"removed" —
// Classify is always Changed for those, never Tolerated/Violation.
// It returns TWO lists. diffs is the findings list every consumer already
// reads. ignored is the headers an `ignore` matcher silenced, which used to
// be dropped on the floor — a `continue` with no record anywhere, making an
// ignored header the only suppression in the engine that left no trace at
// all (the body plane has kept BodyIgnored for exactly this reason). They
// are deliberately kept OUT of diffs rather than added to it with a new
// Type: classify() treats every non-"tolerated" HeaderDiff as a real
// change, and triage reads the same list, so folding them in would turn
// every ignored header into both a "changed" entry and a triage signal.
func DiffHeaders(a, b map[string]string, res rules.Resolved, scope string) (diffs, ignored []HeaderDiff) {
	la, lb := lowerHeaders(a), lowerHeaders(b)
	seen := map[string]bool{}
	var names []string
	for k := range la {
		if !seen[k] {
			seen[k] = true
			names = append(names, k)
		}
	}
	for k := range lb {
		if !seen[k] {
			seen[k] = true
			names = append(names, k)
		}
	}
	sort.Strings(names)

	var out []HeaderDiff
	for _, name := range names {
		av, aok := la[name]
		bv, bok := lb[name]
		if aok && bok && av == bv {
			continue
		}
		bothPresent := aok && bok
		m := res.ForHeader(name)
		outcome := rules.Classify(m, av, bv, bothPresent)
		if outcome == rules.Ignored {
			ignored = append(ignored, HeaderDiff{
				Scope: scope, Name: name, A: av, B: bv,
				Type: "ignored", Matcher: m.Label(),
			})
			continue
		}
		hd := HeaderDiff{Scope: scope, Name: name, A: av, B: bv}
		switch {
		case bothPresent && outcome == rules.Violation:
			hd.Type = "violation"
		case bothPresent && outcome == rules.Tolerated:
			hd.Type = "tolerated"
		case bothPresent:
			hd.Type = "changed"
		case aok:
			hd.Type = "removed"
		default:
			hd.Type = "added"
		}
		if outcome == rules.Tolerated || outcome == rules.Violation {
			hd.Matcher = m.Label()
		}
		out = append(out, hd)
	}
	return out, ignored
}

func buildEntry(p Pair, res rules.Resolved, o Options) Entry {
	e := Entry{
		Method:         p.Method,
		NormalizedPath: p.NormalizedPath,
		SeqA:           p.A.Seq,
		SeqB:           p.B.Seq,
	}
	if p.A.Status != p.B.Status {
		e.StatusChange = &StatusChange{A: p.A.Status, B: p.B.Status}
	}
	// Truncated is the banner ("at least one of the four payloads was
	// size-capped"), but it is NOT a gate on diffing — the gate is per
	// payload, inside parseBody. Ruling: a truncated request body must
	// never silence the response diff (or vice versa). diffBodyScope always
	// runs for both scopes; parseBody's own p.Truncated check (previously
	// dead code — this entry-level gate always intercepted first) makes a
	// truncated payload fall back to the same whole-string comparison used
	// for non-JSON bodies, never a field tree over half-parsed data, while
	// the OTHER three payloads diff normally.
	e.Truncated = p.A.Req.Truncated || p.B.Req.Truncated || p.A.Resp.Truncated || p.B.Resp.Truncated
	ctx := diffCtx{res: res, ignore: o.WireIgnore}
	acc := &bodyAcc{}
	diffBodyScope("req", p.A.Req, p.B.Req, ctx, acc)
	diffBodyScope("resp", p.A.Resp, p.B.Resp, ctx, acc)
	e.BodyDiff = acc.Diff
	e.BodyTolerated = acc.Tolerated
	e.BodyViolations = acc.Violations
	e.BodyIgnored = acc.Ignored
	e.OrderingChanges = acc.Ordering
	reqHeaders, reqIgnored := DiffHeaders(p.A.Req.Headers, p.B.Req.Headers, res, "req")
	respHeaders, respIgnored := DiffHeaders(p.A.Resp.Headers, p.B.Resp.Headers, res, "resp")
	e.HeaderDiff = append(reqHeaders, respHeaders...)
	e.HeaderIgnored = append(reqIgnored, respIgnored...)
	return e
}

func callsFrom(hops []trace.Hop, groups []runs.Group) []Call {
	out := make([]Call, len(hops))
	for i, h := range hops {
		path, _ := SplitPath(h.Path)
		out[i] = Call{
			Method: h.Method,
			Path:   path,
			Seq:    h.Seq,
			Status: h.Status,
			Group:  runs.GroupAt(groups, h.T.Start),
		}
	}
	return out
}

// DiffWire pairs a's and b's calls, diffs every pair field by field, and
// annotates the paired result with per-side ordinals and reorder detection
// (order.go). Options.Deviations is not consulted here — Task 11 owns
// applying the ledger to Call.Tolerated; a nil (or even populated) value is
// a no-op in this task, so a run before Task 11 exists never tolerates
// anything through this path.
func DiffWire(a, b []trace.Hop, o Options) Wire {
	normalize := o.Normalize
	if normalize == nil {
		normalize = func(p string) string { return p }
	}
	pairs, missingHops, extraHops := PairCalls(a, b, normalize)

	entries := make([]Entry, len(pairs))
	for i, p := range pairs {
		res := rules.Resolve(o.Rules, p.Method, p.NormalizedPath)
		e := buildEntry(p, res, o)
		e.GroupA = runs.GroupAt(o.GroupsA, p.A.T.Start)
		e.GroupB = runs.GroupAt(o.GroupsB, p.B.T.Start)
		entries[i] = e
	}
	entries = annotate(entries)

	var groupsPtr *GroupNames
	namesA, namesB := runs.GroupNames(o.GroupsA), runs.GroupNames(o.GroupsB)
	if len(namesA) > 0 || len(namesB) > 0 {
		groupsPtr = &GroupNames{A: namesA, B: namesB}
	}

	return Wire{
		Paired: entries,
		// A matched deviation ANNOTATES the call; it never removes it. See
		// applyDeviations in deviations.go.
		Missing: applyDeviations(callsFrom(missingHops, o.GroupsA), o.Deviations),
		Extra:   applyDeviations(callsFrom(extraHops, o.GroupsB), o.Deviations),
		Groups:  groupsPtr,
	}
}
