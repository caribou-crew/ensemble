package diff

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/retrace/rules"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

func mustRules(t *testing.T, raw []rules.Raw) []rules.Rule {
	t.Helper()
	rs, err := rules.Normalize(raw)
	if err != nil {
		t.Fatalf("rules.Normalize(%+v): %v", raw, err)
	}
	return rs
}

func hop(seq uint64, method, path string, status int, reqBody, respBody string) trace.Hop {
	return trace.Hop{
		Seq:    seq,
		Method: method,
		Path:   path,
		Status: status,
		Req:    trace.Payload{Body: reqBody},
		Resp:   trace.Payload{Body: respBody},
	}
}

func mustMatcher(t *testing.T, spec any) rules.Matcher {
	t.Helper()
	m, err := rules.ParseMatcher(spec, "test")
	if err != nil {
		t.Fatalf("ParseMatcher(%v): %v", spec, err)
	}
	return m
}

// normalizeCartID is a minimal test-only path normalizer, standing in for
// config.NormalizePath, so pairing tests can prove NormalizedPath is
// derived from the normalizer's output and not the raw path.
func normalizeCartID(p string) string {
	if strings.HasPrefix(p, "/cart/") {
		return "/cart/:id"
	}
	return p
}

// --- Step 1: pairing ---

func TestPairsOnMethodAndNormalizedPathAndQuery(t *testing.T) {
	a := []trace.Hop{hop(1, "GET", "/cart/42?x=1", 200, "", `{"n":1}`)}
	b := []trace.Hop{hop(1, "GET", "/cart/99?x=1", 200, "", `{"n":1}`)}
	pairs, missing, extra := PairCalls(a, b, normalizeCartID)
	if len(pairs) != 1 {
		t.Fatalf("len(pairs) = %d, want 1", len(pairs))
	}
	if pairs[0].NormalizedPath != "/cart/:id" {
		t.Fatalf("NormalizedPath = %q, want /cart/:id", pairs[0].NormalizedPath)
	}
	// A and B use deliberately DIFFERENT raw paths (42 vs 99) so a Pair.A /
	// Pair.B field swap inside align()/PairCalls is actually observable —
	// a fixture where both sides carry the same path could not catch that.
	if pairs[0].A.Path != "/cart/42?x=1" || pairs[0].B.Path != "/cart/99?x=1" {
		t.Fatalf("pair = {A.Path:%q B.Path:%q}, want A from side a (42), B from side b (99)", pairs[0].A.Path, pairs[0].B.Path)
	}
	if len(missing) != 0 || len(extra) != 0 {
		t.Fatalf("missing=%d extra=%d, want 0, 0", len(missing), len(extra))
	}

	// A different METHOD at the same normalized path must never pair —
	// bucketing is method + path + query, not path alone.
	c := []trace.Hop{hop(1, "POST", "/cart/42?x=1", 200, "", `{"n":1}`)}
	pairs2, missing2, extra2 := PairCalls(a, c, normalizeCartID)
	if len(pairs2) != 0 || len(missing2) != 1 || len(extra2) != 1 {
		t.Fatalf("GET and POST paired across method: pairs=%d missing=%d extra=%d", len(pairs2), len(missing2), len(extra2))
	}
}

func TestQueryParamOrderDoesNotAffectPairing(t *testing.T) {
	a := []trace.Hop{hop(1, "GET", "/search?b=2&a=1", 200, "", "x")}
	b := []trace.Hop{hop(1, "GET", "/search?a=1&b=2", 200, "", "x")}
	pairs, missing, extra := PairCalls(a, b, nil)
	if len(pairs) != 1 || len(missing) != 0 || len(extra) != 0 {
		t.Fatalf("pairs=%d missing=%d extra=%d, want 1, 0, 0", len(pairs), len(missing), len(extra))
	}
}

func TestTheBestMatchWinsNotWhicheverSharedAnIndex(t *testing.T) {
	// three POSTs to /cart with bodies X, Y, Z on side A and Y, Z on side B.
	// A positional zip would pair X↔Y and Y↔Z and report two changes plus a
	// missing; alignment must pair Y↔Y, Z↔Z and report X missing.
	//
	// Bodies are multi-character JSON, each near-but-not-exactly-equal to
	// its intended match (F7) — a single-character body ("X"/"Y"/"Z") hits
	// diceBigram's len<2 cliff and short-circuits on exact equality only,
	// so it never actually exercises the bigram scorer; these bodies force
	// canonicalBodyForSimilarity's ca==cb fast path to miss and the real
	// bigram-overlap computation to decide the alignment.
	//
	// A and B use NON-OVERLAPPING Seq ranges (101-103 vs 201-202) — CallSimilarity
	// never looks at Seq, so this doesn't perturb the algorithm under test,
	// but it means a Pair.A/Pair.B field swap is independently observable
	// even though the matched bodies read the same on both sides.
	a := []trace.Hop{
		hop(101, "POST", "/cart", 200, `{"item":"alpha-order-0000"}`, ""),
		hop(102, "POST", "/cart", 200, `{"item":"bravo-order-0000"}`, ""),
		hop(103, "POST", "/cart", 200, `{"item":"charlie-order-0000"}`, ""),
	}
	b := []trace.Hop{
		hop(201, "POST", "/cart", 200, `{"item":"bravo-order-0001"}`, ""),
		hop(202, "POST", "/cart", 200, `{"item":"charlie-order-0001"}`, ""),
	}
	pairs, missing, extra := PairCalls(a, b, nil)
	if len(pairs) != 2 {
		t.Fatalf("len(pairs) = %d, want 2", len(pairs))
	}
	for _, p := range pairs {
		aWord := strings.SplitN(p.A.Req.Body, "-", 2)[0]
		bWord := strings.SplitN(p.B.Req.Body, "-", 2)[0]
		if aWord != bWord {
			t.Fatalf("pair mismatched bodies: A=%q B=%q, want same-word pairing (bravo<->bravo, charlie<->charlie)", p.A.Req.Body, p.B.Req.Body)
		}
		if p.A.Seq < 101 || p.A.Seq > 103 {
			t.Fatalf("p.A.Seq = %d, want a side-a seq (101-103) — Pair.A must hold the side-a hop", p.A.Seq)
		}
		if p.B.Seq < 201 || p.B.Seq > 202 {
			t.Fatalf("p.B.Seq = %d, want a side-b seq (201-202) — Pair.B must hold the side-b hop", p.B.Seq)
		}
	}
	if len(missing) != 1 || !strings.Contains(missing[0].Req.Body, "alpha") {
		t.Fatalf("missing = %+v, want [alpha]", missing)
	}
	if len(extra) != 0 {
		t.Fatalf("extra = %+v, want none", extra)
	}
}

func TestDifferentQueryValuesPreventPairing(t *testing.T) {
	// M6/F11: bucketKey must include the query, and a query VALUE
	// difference (not just param order, see TestQueryParamOrderDoesNotAffectPairing)
	// must fall through to Missing/Extra rather than pairing.
	a := []trace.Hop{hop(1, "GET", "/search?q=shoes", 200, "", "x")}
	b := []trace.Hop{hop(1, "GET", "/search?q=hats", 200, "", "x")}
	pairs, missing, extra := PairCalls(a, b, nil)
	if len(pairs) != 0 || len(missing) != 1 || len(extra) != 1 {
		t.Fatalf("pairs=%d missing=%d extra=%d, want 0,1,1 — different query VALUES must not pair", len(pairs), len(missing), len(extra))
	}
}

func TestZeroSimilarityCallsStillPairViaTheGreaterOrEqualTieBreak(t *testing.T) {
	// F16/M7: align's tie-break is >= (a pair is made whenever it beats
	// leaving both sides unmatched, per the brief's own prose). Two calls
	// that share nothing — different status, and bodies with zero bigram
	// overlap on both req and resp — still pair rather than falling to
	// Missing/Extra.
	a := []trace.Hop{hop(1, "GET", "/x", 200, "aaaa", "bbbb")}
	b := []trace.Hop{hop(1, "GET", "/x", 404, "zzzz", "yyyy")}
	pairs, missing, extra := PairCalls(a, b, nil)
	if len(pairs) != 1 || len(missing) != 0 || len(extra) != 0 {
		t.Fatalf("pairs=%d missing=%d extra=%d, want 1,0,0 — zero-similarity calls in the same bucket must still pair under >=", len(pairs), len(missing), len(extra))
	}
}

func TestResponseBodyCarriesMoreSimilarityWeightThanRequestBody(t *testing.T) {
	// M8: swapping CallSimilarity's brief-specified 0.5 resp / 0.2 req
	// weights. A candidate matching on response body must score higher
	// than one matching on request body by the same margin.
	base := hop(1, "POST", "/x", 200, "reqA", "respA")
	sameResp := hop(2, "POST", "/x", 200, "reqB", "respA")
	sameReq := hop(3, "POST", "/x", 200, "reqA", "respB")
	simResp := CallSimilarity(base, sameResp)
	simReq := CallSimilarity(base, sameReq)
	if !(simResp > simReq) {
		t.Fatalf("CallSimilarity(matching resp)=%v, CallSimilarity(matching req)=%v, want matching-resp strictly higher (0.5 > 0.2 weight)", simResp, simReq)
	}
}

func TestBodySimilarityOrderingProperties(t *testing.T) {
	// F7: pins ordering properties of bodySimilarity/diceBigram — pairing's
	// foundation — rather than just its existence.
	if got := bodySimilarity("hello world", "hello world"); got != 1 {
		t.Fatalf("sim(x,x) = %v, want 1", got)
	}
	near := bodySimilarity("hello world", "hello worlds")
	unrelated := bodySimilarity("hello world", "goodbye moon zz")
	if !(near > unrelated) {
		t.Fatalf("sim(x,near-x)=%v want > sim(x,unrelated)=%v", near, unrelated)
	}
	// M9: skipping JSON canonicalization — a reordered-key JSON object must
	// still score a perfect match.
	reordered := bodySimilarity(`{"a":1,"b":2}`, `{"b":2,"a":1}`)
	if reordered != 1 {
		t.Fatalf("sim of JSON key-reorder = %v, want 1 (canonicalization)", reordered)
	}
	valueChanged := bodySimilarity(`{"a":1,"b":2}`, `{"a":1,"b":3}`)
	if !(valueChanged < 1) {
		t.Fatalf("sim of value-changed JSON = %v, want < 1", valueChanged)
	}
}

func TestDiceBigramIsNotAConstant(t *testing.T) {
	// M10: diceBigram degraded to `return 1` unconditionally.
	if got := diceBigram("abcdefgh", "abcdefgh"); got != 1 {
		t.Fatalf("diceBigram(x,x) = %v, want 1", got)
	}
	if got := diceBigram("aaaaaaaa", "zzzzzzzz"); got != 0 {
		t.Fatalf("diceBigram(disjoint) = %v, want 0", got)
	}
	if got := diceBigram("abcdefgh", "abcdxyzh"); got <= 0 || got >= 1 {
		t.Fatalf("diceBigram(partial overlap) = %v, want strictly between 0 and 1", got)
	}
}

func TestStatusCarriesWeightInSimilarity(t *testing.T) {
	// a 304 with no body must not pair with a 200 with a body when a
	// better 200 candidate exists. Both candidates' bodies are made
	// EQUALLY (zero-)similar to A's body on purpose — an empty body always
	// scores 0 against a non-empty one, and "xyz" shares no bigrams with
	// "hello" either — so body similarity alone is a dead tie and only the
	// status term can correctly break it. The 304 (wrong status) is listed
	// FIRST, so a body-similarity tie without the status term would
	// resolve to it via align()'s left-to-right tie-break, proving this
	// test can't pass by accident.
	a := []trace.Hop{hop(1, "GET", "/x", 200, "", "hello")}
	b := []trace.Hop{
		hop(1, "GET", "/x", 304, "", ""),
		hop(2, "GET", "/x", 200, "", "xyz"),
	}
	pairs, _, extra := PairCalls(a, b, nil)
	if len(pairs) != 1 {
		t.Fatalf("len(pairs) = %d, want 1", len(pairs))
	}
	if pairs[0].B.Status != 200 {
		t.Fatalf("paired with status %d (seq %d), want the 200 candidate over the 304 despite equal body similarity", pairs[0].B.Status, pairs[0].B.Seq)
	}
	if len(extra) != 1 || extra[0].Status != 304 {
		t.Fatalf("extra = %+v, want the unmatched 304", extra)
	}
}

func TestUnmatchedCallsFallThroughToMissingAndExtra(t *testing.T) {
	a := []trace.Hop{hop(1, "GET", "/only-a", 200, "", "")}
	b := []trace.Hop{hop(1, "GET", "/only-b", 200, "", "")}
	pairs, missing, extra := PairCalls(a, b, nil)
	if len(pairs) != 0 {
		t.Fatalf("len(pairs) = %d, want 0", len(pairs))
	}
	if len(missing) != 1 || missing[0].Path != "/only-a" {
		t.Fatalf("missing = %+v", missing)
	}
	if len(extra) != 1 || extra[0].Path != "/only-b" {
		t.Fatalf("extra = %+v", extra)
	}
}

func TestIdenticalRunsPairOneToOneInOrder(t *testing.T) {
	// ties resolve toward the diagonal. A and B use non-overlapping Seq
	// ranges (101-103 vs 201-203, same offsets) — CallSimilarity ignores
	// Seq, so every ai/bj pair is still equally similar (the actual point
	// of the test), but a same-numbered fixture (both sides 1,2,3) could
	// not tell a correct diagonal pairing from a Pair.A/Pair.B field swap,
	// since a[i].Seq == b[i].Seq at every diagonal position either way.
	var a, b []trace.Hop
	for i := uint64(1); i <= 3; i++ {
		a = append(a, hop(100+i, "GET", "/x", 200, "", "same"))
		b = append(b, hop(200+i, "GET", "/x", 200, "", "same"))
	}
	pairs, _, _ := PairCalls(a, b, nil)
	if len(pairs) != 3 {
		t.Fatalf("len(pairs) = %d, want 3", len(pairs))
	}
	for i, p := range pairs {
		wantA, wantB := uint64(101+i), uint64(201+i)
		if p.A.Seq != wantA || p.B.Seq != wantB {
			t.Fatalf("pair %d: A.Seq=%d B.Seq=%d, want %d, %d (diagonal)", i, p.A.Seq, p.B.Seq, wantA, wantB)
		}
	}
}

// --- Step 5/6: field-level diff ---

func TestAToleratedBodyFieldIsReportedSeparatelyNotAsAChange(t *testing.T) {
	res := rules.Resolved{Body: []rules.BodyRule{{Glob: "id", Matcher: mustMatcher(t, "uuid")}}}
	a := trace.Payload{Body: `{"id":"11111111-1111-1111-1111-111111111111","name":"x"}`}
	b := trace.Payload{Body: `{"id":"22222222-2222-2222-2222-222222222222","name":"x"}`}
	acc := &bodyAcc{}
	diffBodyScope("resp", a, b, diffCtx{res: res}, acc)
	if len(acc.Diff) != 0 {
		t.Fatalf("acc.Diff = %+v, want empty — a tolerated field must not also appear as a plain change", acc.Diff)
	}
	if len(acc.Tolerated) != 1 || acc.Tolerated[0].Path != "id" || acc.Tolerated[0].Matcher != "uuid" {
		t.Fatalf("acc.Tolerated = %+v, want one entry at path id with matcher uuid", acc.Tolerated)
	}
}

func TestAViolatingBodyFieldIsRecordedAsAViolationNotAPlainChange(t *testing.T) {
	res := rules.Resolved{Body: []rules.BodyRule{{Glob: "id", Matcher: mustMatcher(t, "uuid")}}}
	a := trace.Payload{Body: `{"id":"not-a-uuid"}`}
	b := trace.Payload{Body: `{"id":"also-not-a-uuid"}`}
	acc := &bodyAcc{}
	diffBodyScope("resp", a, b, diffCtx{res: res}, acc)
	if len(acc.Diff) != 0 {
		t.Fatalf("acc.Diff = %+v, want empty", acc.Diff)
	}
	if len(acc.Violations) != 1 || acc.Violations[0].Path != "id" {
		t.Fatalf("acc.Violations = %+v, want one entry at path id", acc.Violations)
	}
}

func TestLastMatchingBodyGlobWinsOverAnEarlierOne(t *testing.T) {
	// Two rules both resolve for path "id": an early broad ignore and a
	// later, more specific uuid matcher. Resolved.ForField's contract (and
	// this package's own resolveField, which must mirror it) is
	// last-match-wins, so the uuid matcher — not the ignore — must govern.
	res := rules.Resolved{Body: []rules.BodyRule{
		{Glob: "*", Matcher: mustMatcher(t, "ignore")},
		{Glob: "id", Matcher: mustMatcher(t, "uuid")},
	}}
	a := trace.Payload{Body: `{"id":"not-a-uuid"}`}
	b := trace.Payload{Body: `{"id":"also-not-a-uuid"}`}
	acc := &bodyAcc{}
	diffBodyScope("resp", a, b, diffCtx{res: res}, acc)
	if len(acc.Ignored) != 0 {
		t.Fatalf("acc.Ignored = %+v, want none — the later, more specific uuid rule must win over the earlier broad ignore", acc.Ignored)
	}
	if len(acc.Violations) != 1 || acc.Violations[0].Matcher != "uuid" {
		t.Fatalf("acc.Violations = %+v, want one uuid violation", acc.Violations)
	}
}

func TestBodyDiffKeepsItsMeaningWhenNoRulesAreConfigured(t *testing.T) {
	a := trace.Payload{Body: `{"name":"a"}`}
	b := trace.Payload{Body: `{"name":"b"}`}
	acc := &bodyAcc{}
	diffBodyScope("resp", a, b, diffCtx{}, acc)
	if len(acc.Diff) != 1 || acc.Diff[0].Path != "name" || acc.Diff[0].A != "a" || acc.Diff[0].B != "b" {
		t.Fatalf("acc.Diff = %+v, want one changed entry at path name, a/b", acc.Diff)
	}
	if len(acc.Tolerated)+len(acc.Violations)+len(acc.Ignored) != 0 {
		t.Fatalf("no rule was configured — nothing should classify as tolerated/violation/ignored: %+v", acc)
	}
}

func TestAnIgnoreIsOnlyRecordedWhenItActuallySuppressedADifference(t *testing.T) {
	res := rules.Resolved{Body: []rules.BodyRule{{Glob: "ts", Matcher: mustMatcher(t, "ignore")}}}

	differing := &bodyAcc{}
	diffBodyScope("resp", trace.Payload{Body: `{"ts":"1"}`}, trace.Payload{Body: `{"ts":"2"}`}, diffCtx{res: res}, differing)
	if len(differing.Ignored) != 1 {
		t.Fatalf("Ignored = %+v, want one entry — the ignore rule DID suppress a real difference", differing.Ignored)
	}

	equal := &bodyAcc{}
	diffBodyScope("resp", trace.Payload{Body: `{"ts":"1"}`}, trace.Payload{Body: `{"ts":"1"}`}, diffCtx{res: res}, equal)
	if len(equal.Ignored) != 0 {
		t.Fatalf("Ignored = %+v, want none — the values never differed, so the ignore rule suppressed nothing", equal.Ignored)
	}
	if len(equal.Diff)+len(equal.Tolerated)+len(equal.Violations) != 0 {
		t.Fatalf("equal values must produce no diff of any kind: %+v", equal)
	}
}

func TestAddedAndRemovedKeysAreReportedAtTheChildPath(t *testing.T) {
	a := trace.Payload{Body: `{"a":1,"removedKey":"gone"}`}
	b := trace.Payload{Body: `{"a":1,"addedKey":"new"}`}
	acc := &bodyAcc{}
	diffBodyScope("req", a, b, diffCtx{}, acc)
	if len(acc.Diff) != 2 {
		t.Fatalf("acc.Diff = %+v, want exactly 2 entries (removedKey, addedKey) — key a is unchanged", acc.Diff)
	}
	byPath := map[string]FieldDiff{}
	for _, fd := range acc.Diff {
		byPath[fd.Path] = fd
	}
	if fd, ok := byPath["removedKey"]; !ok || fd.Type != "removed" || fd.A != "gone" {
		t.Fatalf("removedKey entry = %+v, ok=%v", fd, ok)
	}
	if fd, ok := byPath["addedKey"]; !ok || fd.Type != "added" || fd.B != "new" {
		t.Fatalf("addedKey entry = %+v, ok=%v", fd, ok)
	}
}

func TestSameMultisetDifferentOrderIsAnOrderingChangeNotEightFieldChanges(t *testing.T) {
	a := trace.Payload{Body: `[{"id":1,"name":"x"},{"id":2,"name":"y"},{"id":3,"name":"z"},{"id":4,"name":"w"}]`}
	b := trace.Payload{Body: `[{"id":3,"name":"z"},{"id":1,"name":"x"},{"id":4,"name":"w"},{"id":2,"name":"y"}]`}
	acc := &bodyAcc{}
	diffBodyScope("resp", a, b, diffCtx{}, acc)
	if len(acc.Ordering) != 1 {
		t.Fatalf("acc.Ordering = %+v, want exactly one ordering-change entry", acc.Ordering)
	}
	if len(acc.Diff) != 0 {
		t.Fatalf("acc.Diff = %+v, want none — a pure reorder must not also report 8 field changes", acc.Diff)
	}
}

func TestAnUnchangedArrayIsNeverReportedAsAnOrderingChange(t *testing.T) {
	// A same-length, IDENTICALLY-ordered array (same multiset trivially,
	// since it's literally the same elements in the same order) sits next
	// to an unrelated field that really did change. Only the real change
	// may be reported — the untouched array must never show up as a
	// spurious ordering change just because "same multiset" was checked
	// without also checking the elements weren't already positionally
	// identical.
	a := trace.Payload{Body: `{"tag":"old","items":[{"id":1},{"id":2},{"id":3}]}`}
	b := trace.Payload{Body: `{"tag":"new","items":[{"id":1},{"id":2},{"id":3}]}`}
	acc := &bodyAcc{}
	diffBodyScope("resp", a, b, diffCtx{}, acc)
	if len(acc.Ordering) != 0 {
		t.Fatalf("acc.Ordering = %+v, want none — items never changed", acc.Ordering)
	}
	if len(acc.Diff) != 1 || acc.Diff[0].Path != "tag" {
		t.Fatalf("acc.Diff = %+v, want exactly one change at path tag", acc.Diff)
	}
}

func TestAReorderIsDetectedWhenAnIgnoredFieldDiffersInEveryElement(t *testing.T) {
	// Same shape as the "tolerated" reorder test, but the excusing rule is
	// an ignore matcher, not a value matcher — blankTolerated must treat
	// KindIgnore as blankable too, not just KindNamed/KindPattern.
	res := rules.Resolved{Body: []rules.BodyRule{{Glob: "*.stamp", Matcher: mustMatcher(t, "ignore")}}}
	a := trace.Payload{Body: `[{"stamp":"t1","sku":"A"},{"stamp":"t2","sku":"B"}]`}
	b := trace.Payload{Body: `[{"stamp":"t3","sku":"B"},{"stamp":"t4","sku":"A"}]`}
	acc := &bodyAcc{}
	diffBodyScope("resp", a, b, diffCtx{res: res}, acc)
	if len(acc.Ordering) != 1 {
		t.Fatalf("acc.Ordering = %+v, want one ordering-change entry despite stamp differing at every position", acc.Ordering)
	}
	if len(acc.Diff) != 0 {
		t.Fatalf("acc.Diff = %+v, want none", acc.Diff)
	}
}

func TestAReorderIsDetectedEvenThoughAToleratedFieldDiffersInEveryElement(t *testing.T) {
	res := rules.Resolved{Body: []rules.BodyRule{{Glob: "*.reqId", Matcher: mustMatcher(t, "uuid")}}}
	a := trace.Payload{Body: `[
		{"reqId":"11111111-1111-1111-1111-111111111111","sku":"A"},
		{"reqId":"22222222-2222-2222-2222-222222222222","sku":"B"}
	]`}
	b := trace.Payload{Body: `[
		{"reqId":"33333333-3333-3333-3333-333333333333","sku":"B"},
		{"reqId":"44444444-4444-4444-4444-444444444444","sku":"A"}
	]`}
	acc := &bodyAcc{}
	diffBodyScope("resp", a, b, diffCtx{res: res}, acc)
	if len(acc.Ordering) != 1 {
		t.Fatalf("acc.Ordering = %+v, want one ordering-change entry despite reqId differing at every position", acc.Ordering)
	}
	if len(acc.Diff) != 0 {
		t.Fatalf("acc.Diff = %+v, want none", acc.Diff)
	}
}

func TestWithoutARuleTheSameReorderStillReportsPositionalChanges(t *testing.T) {
	// Identical fixture to the test above, MINUS the uuid rule: with nothing
	// to blank the per-element reqId, the arrays are not the same multiset,
	// so this must fall back to ordinary positional field diffs.
	a := trace.Payload{Body: `[
		{"reqId":"11111111-1111-1111-1111-111111111111","sku":"A"},
		{"reqId":"22222222-2222-2222-2222-222222222222","sku":"B"}
	]`}
	b := trace.Payload{Body: `[
		{"reqId":"33333333-3333-3333-3333-333333333333","sku":"B"},
		{"reqId":"44444444-4444-4444-4444-444444444444","sku":"A"}
	]`}
	acc := &bodyAcc{}
	diffBodyScope("resp", a, b, diffCtx{}, acc)
	if len(acc.Ordering) != 0 {
		t.Fatalf("acc.Ordering = %+v, want none — no rule bridges the reqId difference", acc.Ordering)
	}
	var skuChanges int
	for _, fd := range acc.Diff {
		if strings.HasSuffix(fd.Path, ".sku") {
			skuChanges++
		}
	}
	if skuChanges != 2 {
		t.Fatalf("acc.Diff = %+v, want 2 positional sku changes", acc.Diff)
	}
}

func TestFieldGlobsAddressArrayElementsWithBracketIndices(t *testing.T) {
	// "items.*.sku" is three dot-segments; the walker emits array elements
	// as "items[0].sku" — two dot-segments — so this glob must NOT match,
	// per rules.MatchFieldGlob's documented bracket-index convention.
	res := rules.Resolved{Body: []rules.BodyRule{{Glob: "items.*.sku", Matcher: mustMatcher(t, "ignore")}}}
	a := trace.Payload{Body: `{"items":[{"sku":"A"}]}`}
	b := trace.Payload{Body: `{"items":[{"sku":"B"}]}`}
	acc := &bodyAcc{}
	diffBodyScope("resp", a, b, diffCtx{res: res}, acc)
	if len(acc.Ignored) != 0 {
		t.Fatalf("acc.Ignored = %+v, want none — items.*.sku must not match items[0].sku", acc.Ignored)
	}
	if len(acc.Diff) != 1 || acc.Diff[0].Path != "items[0].sku" {
		t.Fatalf("acc.Diff = %+v, want one changed entry at items[0].sku", acc.Diff)
	}
}

func TestWireIgnoreSuppressesABodyFieldDiff(t *testing.T) {
	ctx := diffCtx{ignore: []string{"ts"}}
	a := trace.Payload{Body: `{"ts":"1","name":"x"}`}
	b := trace.Payload{Body: `{"ts":"2","name":"x"}`}
	acc := &bodyAcc{}
	diffBodyScope("resp", a, b, ctx, acc)
	if len(acc.Ignored) != 1 || acc.Ignored[0].Path != "ts" {
		t.Fatalf("acc.Ignored = %+v, want one entry at path ts", acc.Ignored)
	}
	if len(acc.Diff) != 0 {
		t.Fatalf("acc.Diff = %+v, want none", acc.Diff)
	}
}

func TestHeadersAreComparedCaseInsensitivelyAndEqualOnesOmitted(t *testing.T) {
	a := map[string]string{"Content-Type": "application/json", "X-Request-Id": "abc"}
	b := map[string]string{"content-type": "application/json", "x-request-id": "xyz"}
	diffs := DiffHeaders(a, b, rules.Resolved{}, "req")
	if len(diffs) != 1 {
		t.Fatalf("diffs = %+v, want exactly 1 — content-type is equal case-insensitively and must be omitted", diffs)
	}
	if diffs[0].Name != "x-request-id" || diffs[0].Type != "changed" {
		t.Fatalf("diffs[0] = %+v", diffs[0])
	}
	// a and b carry different values on purpose (abc vs xyz, not a
	// symmetric fixture) — this catches an accidental a/b argument swap
	// inside DiffHeaders, which an equal-valued fixture could not.
	if diffs[0].A != "abc" || diffs[0].B != "xyz" {
		t.Fatalf("diffs[0] = %+v, want A=abc B=xyz", diffs[0])
	}
}

func TestAHeaderOnOneSideOnlyIsNeverToleratedByAValueMatcher(t *testing.T) {
	res := rules.Resolved{Headers: map[string]rules.Matcher{"etag": mustMatcher(t, "etag")}}
	a := map[string]string{"etag": `"abc"`}
	b := map[string]string{}
	diffs := DiffHeaders(a, b, res, "resp")
	if len(diffs) != 1 || diffs[0].Type != "removed" {
		t.Fatalf("diffs = %+v, want one removed entry", diffs)
	}
	if diffs[0].Matcher != "" {
		t.Fatalf("Matcher = %q, want empty — a value matcher must never label a one-sided header as tolerated", diffs[0].Matcher)
	}
}

func TestIgnoreDoesSilenceAnAppearingHeader(t *testing.T) {
	res := rules.Resolved{Headers: map[string]rules.Matcher{"x-trace-id": mustMatcher(t, "ignore")}}
	a := map[string]string{}
	b := map[string]string{"x-trace-id": "newvalue"}
	diffs := DiffHeaders(a, b, res, "resp")
	if len(diffs) != 0 {
		t.Fatalf("diffs = %+v, want none — an ignore rule must silence an appearing header", diffs)
	}
}

// TestHeaderDiffTypeReflectsTheRuleOutcomeNotJustChanged pins F6: a
// tolerated header change and a violating one used to be indistinguishable
// (both Type: "changed"). Type must now carry the outcome — Task 10's
// exit-2-on-Violation bullet is inexpressible for headers otherwise.
func TestHeaderDiffTypeReflectsTheRuleOutcomeNotJustChanged(t *testing.T) {
	res := rules.Resolved{Headers: map[string]rules.Matcher{"etag": mustMatcher(t, "etag")}}

	tolerated := DiffHeaders(map[string]string{"etag": `"abc"`}, map[string]string{"etag": `"def"`}, res, "resp")
	if len(tolerated) != 1 || tolerated[0].Type != "tolerated" {
		t.Fatalf("tolerated etag change = %+v, want Type=tolerated", tolerated)
	}

	violation := DiffHeaders(map[string]string{"etag": "garbage"}, map[string]string{"etag": "junk"}, res, "resp")
	if len(violation) != 1 || violation[0].Type != "violation" {
		t.Fatalf("violating etag change = %+v, want Type=violation", violation)
	}

	plain := DiffHeaders(map[string]string{"x": "1"}, map[string]string{"x": "2"}, rules.Resolved{}, "resp")
	if len(plain) != 1 || plain[0].Type != "changed" {
		t.Fatalf("unruled header change = %+v, want Type=changed", plain)
	}
}

func TestAddedOrRemovedBodyFieldIsNeverTreatedAsBothPresent(t *testing.T) {
	// M13/F15: bothPresent must be false for an added/removed field — a
	// value matcher cannot speak to a value that doesn't exist on one side.
	// If bothPresent were wrongly forced true, a valid-looking value on the
	// present side would classify as Tolerated instead of the correct
	// Changed.
	res := rules.Resolved{Body: []rules.BodyRule{{Glob: "id", Matcher: mustMatcher(t, "uuid")}}}
	a := trace.Payload{Body: `{"id":"11111111-1111-1111-1111-111111111111"}`}
	b := trace.Payload{Body: `{}`}
	acc := &bodyAcc{}
	diffBodyScope("resp", a, b, diffCtx{res: res}, acc)
	if len(acc.Tolerated) != 0 {
		t.Fatalf("acc.Tolerated = %+v, want none — a removed field must never be tolerated by a value matcher", acc.Tolerated)
	}
	if len(acc.Diff) != 1 || acc.Diff[0].Type != "removed" {
		t.Fatalf("acc.Diff = %+v, want one removed entry", acc.Diff)
	}
}

func TestOneSideUnparseableBodyFallsBackToWholeString(t *testing.T) {
	// M17/F10: `!aok || !bok` mutated to `!aok && !bok` would let a
	// one-sided-unparseable pair fall into walk() instead of the brief's
	// mandated whole-string fallback. walk() still produces a single
	// Path=="" entry here (the unparsed side type-asserts to neither []any
	// nor map[string]any, so it hits walk's own scalar fallback) — count
	// and Path alone don't distinguish the two paths, so this pins the
	// VALUES: the whole-string fallback reports the raw Body strings on
	// both sides, never a still-nil A or an already-decoded (map[string]any)
	// B the way walk()'s fallback would.
	a := trace.Payload{Body: `not json`}
	b := trace.Payload{Body: `{"a":1}`}
	acc := &bodyAcc{}
	diffBodyScope("resp", a, b, diffCtx{}, acc)
	if len(acc.Diff) != 1 || acc.Diff[0].Path != "" {
		t.Fatalf("acc.Diff = %+v, want one whole-body diff at Path \"\" when only one side parses as JSON", acc.Diff)
	}
	if acc.Diff[0].A != a.Body || acc.Diff[0].B != b.Body {
		t.Fatalf("acc.Diff[0] = %+v, want A=%q B=%q — the raw body strings, not a half-parsed value", acc.Diff[0], a.Body, b.Body)
	}
}

func TestEqualNonJSONBodiesProduceNoDiff(t *testing.T) {
	// M18/F10: dropping the aPayload.Body != bPayload.Body guard would make
	// every pair of equal non-JSON bodies report a spurious change.
	a := trace.Payload{Body: "same text"}
	b := trace.Payload{Body: "same text"}
	acc := &bodyAcc{}
	diffBodyScope("resp", a, b, diffCtx{}, acc)
	if len(acc.Diff) != 0 {
		t.Fatalf("acc.Diff = %+v, want none — equal non-JSON bodies must not report a spurious change", acc.Diff)
	}
}

func TestBothEmptyBodiesProduceNoDiff(t *testing.T) {
	// F10: empty request bodies are the norm for GETs; two empty bodies
	// must not report a change (same M18 guard, empty-string case).
	acc := &bodyAcc{}
	diffBodyScope("req", trace.Payload{}, trace.Payload{}, diffCtx{}, acc)
	if len(acc.Diff) != 0 {
		t.Fatalf("acc.Diff = %+v, want none for two empty bodies", acc.Diff)
	}
}

func TestArrayLengthDecreaseReportsRemovedTailElements(t *testing.T) {
	// M21/F9: dropping diffArrays' length-tail "removed" loop.
	a := trace.Payload{Body: `{"items":[1,2,3]}`}
	b := trace.Payload{Body: `{"items":[1,2]}`}
	acc := &bodyAcc{}
	diffBodyScope("resp", a, b, diffCtx{}, acc)
	var removed int
	for _, fd := range acc.Diff {
		if fd.Type == "removed" {
			removed++
		}
	}
	if removed != 1 || len(acc.Diff) != 1 {
		t.Fatalf("acc.Diff = %+v, want exactly one removed entry (items[2])", acc.Diff)
	}
}

func TestArrayLengthIncreaseReportsAddedTailElements(t *testing.T) {
	// M22/F9: dropping diffArrays' length-tail "added" loop.
	a := trace.Payload{Body: `{"items":[1,2]}`}
	b := trace.Payload{Body: `{"items":[1,2,3]}`}
	acc := &bodyAcc{}
	diffBodyScope("resp", a, b, diffCtx{}, acc)
	var added int
	for _, fd := range acc.Diff {
		if fd.Type == "added" {
			added++
		}
	}
	if added != 1 || len(acc.Diff) != 1 {
		t.Fatalf("acc.Diff = %+v, want exactly one added entry (items[2])", acc.Diff)
	}
}

func TestSameMultisetRequiresMatchingDuplicateCounts(t *testing.T) {
	// M26/F9: degrading sameMultiset to a set comparison. ["x","x","y"] and
	// ["x","y","y"] share the same SET but differ in duplicate counts — a
	// real field change, not a reorder.
	a := trace.Payload{Body: `{"tags":["x","x","y"]}`}
	b := trace.Payload{Body: `{"tags":["x","y","y"]}`}
	acc := &bodyAcc{}
	diffBodyScope("resp", a, b, diffCtx{}, acc)
	if len(acc.Ordering) != 0 {
		t.Fatalf("acc.Ordering = %+v, want none — duplicate counts differ, this is not a pure reorder", acc.Ordering)
	}
	if len(acc.Diff) == 0 {
		t.Fatalf("acc.Diff = %+v, want at least one positional change", acc.Diff)
	}
}

func TestBlankedPlaceholderIsADistinctiveSentinelNotEmptyString(t *testing.T) {
	// V10: blankedPlaceholder degraded to "". A blanked leaf must never
	// canonicalize to a value real, un-blanked data could also hold.
	res := rules.Resolved{Body: []rules.BodyRule{{Glob: "id", Matcher: mustMatcher(t, "ignore")}}}
	got := blankTolerated("anything", diffCtx{res: res}, "id")
	if got == "" {
		t.Fatalf("blankTolerated returned the empty string — must be a distinctive sentinel, not a value real data could hold")
	}
	if got != blankedPlaceholder {
		t.Fatalf("blankTolerated = %v, want the blankedPlaceholder constant", got)
	}
}

func TestStatusChangeReportsAThenBNotSwapped(t *testing.T) {
	// V6: StatusChange{A: p.B.Status, B: p.A.Status} swap.
	p := Pair{Method: "GET", NormalizedPath: "/x", A: trace.Hop{Status: 200}, B: trace.Hop{Status: 500}}
	e := buildEntry(p, rules.Resolved{}, Options{})
	if e.StatusChange == nil || e.StatusChange.A != 200 || e.StatusChange.B != 500 {
		t.Fatalf("StatusChange = %+v, want A=200 B=500", e.StatusChange)
	}
}

func TestOrderingChangeFieldDiffReportsAThenBNotSwapped(t *testing.T) {
	// V7: OrderingChanges' FieldDiff{A: b, B: a} swap.
	a := trace.Payload{Body: `[{"id":1},{"id":2}]`}
	b := trace.Payload{Body: `[{"id":2},{"id":1}]`}
	acc := &bodyAcc{}
	diffBodyScope("resp", a, b, diffCtx{}, acc)
	if len(acc.Ordering) != 1 {
		t.Fatalf("acc.Ordering = %+v, want 1 entry", acc.Ordering)
	}
	fd := acc.Ordering[0]
	aArr, _ := fd.A.([]any)
	bArr, _ := fd.B.([]any)
	if len(aArr) == 0 || len(bArr) == 0 {
		t.Fatalf("fd = %+v, want non-empty A and B arrays", fd)
	}
	firstA, _ := aArr[0].(map[string]any)
	firstB, _ := bArr[0].(map[string]any)
	if firstA["id"] != float64(1) || firstB["id"] != float64(2) {
		t.Fatalf("fd.A[0]=%v fd.B[0]=%v, want A from side a (id:1) and B from side b (id:2)", firstA, firstB)
	}
}

// TestAReorderNeverSilencesACoOccurringRuleViolation pins F2 through
// DiffWire, not the internals: blankTolerated used to blank a per-element
// field regardless of whether the value satisfied its own matcher, so a
// genuine rule Violation vanished into a benign OrderingChanges entry
// whenever it co-occurred with a reorder. A Violation and a reorder are
// independent facts and neither may silently swallow the other.
func TestAReorderNeverSilencesACoOccurringRuleViolation(t *testing.T) {
	rs := mustRules(t, []rules.Raw{{Body: map[string]any{"*.reqId": "uuid"}}})
	a := []trace.Hop{hop(1, "GET", "/x", 200, "", `[
		{"reqId":"11111111-1111-1111-1111-111111111111","sku":"A"},
		{"reqId":"not-a-uuid-1","sku":"B"}
	]`)}
	b := []trace.Hop{hop(1, "GET", "/x", 200, "", `[
		{"reqId":"not-a-uuid-2","sku":"B"},
		{"reqId":"22222222-2222-2222-2222-222222222222","sku":"A"}
	]`)}
	w := DiffWire(a, b, Options{Rules: rs})
	if len(w.Paired) != 1 {
		t.Fatalf("len(Paired) = %d, want 1", len(w.Paired))
	}
	e := w.Paired[0]
	if len(e.BodyViolations) == 0 {
		t.Fatalf("BodyViolations = %+v, want at least one — the reqId violation must survive the co-occurring reorder", e.BodyViolations)
	}
}

// TestATruncatedRequestBodyDoesNotSilenceACompletelyChangedResponseBody
// pins F1 through DiffWire: a truncated request body must never gate out
// diffing of the response body on the same entry, and the entry must never
// classify as "identical" when the comparison did not fully happen.
func TestATruncatedRequestBodyDoesNotSilenceACompletelyChangedResponseBody(t *testing.T) {
	a := []trace.Hop{{
		Seq: 1, Method: "POST", Path: "/orders", Status: 200,
		Req:  trace.Payload{Body: `{"big":"...huge request body..."}`, Truncated: true},
		Resp: trace.Payload{Body: `{"total":10}`},
	}}
	b := []trace.Hop{{
		Seq: 1, Method: "POST", Path: "/orders", Status: 200,
		Req:  trace.Payload{Body: `{"big":"...different huge request body..."}`, Truncated: true},
		Resp: trace.Payload{Body: `{"total":9999}`},
	}}
	w := DiffWire(a, b, Options{})
	if len(w.Paired) != 1 {
		t.Fatalf("len(Paired) = %d, want 1", len(w.Paired))
	}
	e := w.Paired[0]
	if !e.Truncated {
		t.Fatalf("Truncated = false, want true")
	}
	var respChanged bool
	for _, fd := range e.BodyDiff {
		if fd.Scope == "resp" {
			respChanged = true
		}
	}
	if !respChanged {
		t.Fatalf("BodyDiff = %+v, want the response's total:10->9999 change to be visible despite the request body being truncated", e.BodyDiff)
	}
	for _, c := range e.Classes {
		if c == "identical" {
			t.Fatalf("Classes = %v, want never \"identical\" for a truncated entry — the comparison did not fully happen", e.Classes)
		}
	}
}

// --- Step 3/12: DiffWire wiring (F3 — the package's entry point had
// effectively no test; all 13 wiring mutations (W1-W13) survived) ---

func TestDiffWireAppliesRulesAndWireIgnoreThroughTheRealResolutionPath(t *testing.T) {
	// W1, W2: Options.Rules and Options.WireIgnore must both reach the
	// walker, built through the real production path (rules.Normalize),
	// not a hand-built rules.Resolved.
	rs := mustRules(t, []rules.Raw{{Body: map[string]any{"id": "uuid"}}})
	a := []trace.Hop{hop(1, "POST", "/orders", 200, "", `{"id":"11111111-1111-1111-1111-111111111111","ts":"1"}`)}
	b := []trace.Hop{hop(1, "POST", "/orders", 200, "", `{"id":"22222222-2222-2222-2222-222222222222","ts":"2"}`)}

	w := DiffWire(a, b, Options{Rules: rs, WireIgnore: []string{"ts"}})
	e := w.Paired[0]
	if len(e.BodyDiff) != 0 {
		t.Fatalf("BodyDiff = %+v, want none — id is tolerated by the uuid rule and ts is wire-ignored", e.BodyDiff)
	}
	if len(e.BodyTolerated) != 1 || e.BodyTolerated[0].Path != "id" {
		t.Fatalf("BodyTolerated = %+v, want one entry at id — Options.Rules must reach the walker (W2)", e.BodyTolerated)
	}
	if len(e.BodyIgnored) != 1 || e.BodyIgnored[0].Path != "ts" {
		t.Fatalf("BodyIgnored = %+v, want one entry at ts — Options.WireIgnore must reach the walker (W1)", e.BodyIgnored)
	}
}

func TestDiffWireAppliesNormalizeToPairingAndNormalizedPath(t *testing.T) {
	// W3: Options.Normalize ignored, pass identity.
	a := []trace.Hop{hop(1, "GET", "/cart/42", 200, "", "x")}
	b := []trace.Hop{hop(1, "GET", "/cart/99", 200, "", "x")}
	w := DiffWire(a, b, Options{Normalize: normalizeCartID})
	if len(w.Paired) != 1 {
		t.Fatalf("Paired = %+v, want 1 pair — Options.Normalize must reach pairing", w.Paired)
	}
	if w.Paired[0].NormalizedPath != "/cart/:id" {
		t.Fatalf("NormalizedPath = %q, want /cart/:id", w.Paired[0].NormalizedPath)
	}
}

func TestDiffWireAssignsGroupsFromTheCorrectSideAndBuildsGroupNames(t *testing.T) {
	// W4: swap GroupsA/GroupsB. W5: nil out both GroupNames results.
	t0 := time.Unix(1000, 0)
	groupsA := []runs.Group{{Name: "browse", StartedAt: t0, EndedAt: t0.Add(time.Hour)}}
	groupsB := []runs.Group{{Name: "checkout", StartedAt: t0, EndedAt: t0.Add(time.Hour)}}
	h := hop(1, "GET", "/x", 200, "", "")
	h.T.Start = t0.Add(time.Minute)
	w := DiffWire([]trace.Hop{h}, []trace.Hop{h}, Options{GroupsA: groupsA, GroupsB: groupsB})
	e := w.Paired[0]
	if e.GroupA != "browse" {
		t.Fatalf("GroupA = %q, want browse (W4)", e.GroupA)
	}
	if e.GroupB != "checkout" {
		t.Fatalf("GroupB = %q, want checkout (W4)", e.GroupB)
	}
	if w.Groups == nil {
		t.Fatalf("Groups = nil, want populated GroupNames (W5)")
	}
	if len(w.Groups.A) != 1 || w.Groups.A[0] != "browse" || len(w.Groups.B) != 1 || w.Groups.B[0] != "checkout" {
		t.Fatalf("Groups = %+v, want A:[browse] B:[checkout]", w.Groups)
	}
}

func TestDiffWireMissingCallsUseGroupsAAndExtraCallsUseGroupsB(t *testing.T) {
	// W7: Missing uses o.GroupsB (should be GroupsA).
	t0 := time.Unix(2000, 0)
	groupsA := []runs.Group{{Name: "browse", StartedAt: t0, EndedAt: t0.Add(time.Hour)}}
	groupsB := []runs.Group{{Name: "checkout", StartedAt: t0, EndedAt: t0.Add(time.Hour)}}
	hA := hop(1, "GET", "/only-a", 200, "", "")
	hA.T.Start = t0.Add(time.Minute)
	hB := hop(1, "GET", "/only-b", 200, "", "")
	hB.T.Start = t0.Add(time.Minute)
	w := DiffWire([]trace.Hop{hA}, []trace.Hop{hB}, Options{GroupsA: groupsA, GroupsB: groupsB})
	if len(w.Missing) != 1 || w.Missing[0].Group != "browse" {
		t.Fatalf("Missing = %+v, want group browse (from GroupsA)", w.Missing)
	}
	if len(w.Extra) != 1 || w.Extra[0].Group != "checkout" {
		t.Fatalf("Extra = %+v, want group checkout (from GroupsB)", w.Extra)
	}
}

func TestDiffWireMissingCallPathExcludesQuery(t *testing.T) {
	// W8: callsFrom emitting the raw path (query included) instead of the
	// split path.
	a := []trace.Hop{hop(1, "GET", "/only-a?x=1&y=2", 200, "", "")}
	w := DiffWire(a, nil, Options{})
	if len(w.Missing) != 1 || w.Missing[0].Path != "/only-a" {
		t.Fatalf("Missing[0].Path = %q, want /only-a (query stripped)", w.Missing[0].Path)
	}
}

func TestDiffWireDiffsBothRequestAndResponseBodies(t *testing.T) {
	// W9: delete the resp body diff. W10: delete the req body diff.
	a := []trace.Hop{hop(1, "POST", "/x", 200, `{"r":1}`, `{"s":1}`)}
	b := []trace.Hop{hop(1, "POST", "/x", 200, `{"r":2}`, `{"s":2}`)}
	w := DiffWire(a, b, Options{})
	e := w.Paired[0]
	var sawReq, sawResp bool
	for _, fd := range e.BodyDiff {
		if fd.Scope == "req" {
			sawReq = true
		}
		if fd.Scope == "resp" {
			sawResp = true
		}
	}
	if !sawReq {
		t.Fatalf("BodyDiff = %+v, want a req-scope diff (W10)", e.BodyDiff)
	}
	if !sawResp {
		t.Fatalf("BodyDiff = %+v, want a resp-scope diff (W9)", e.BodyDiff)
	}
}

func TestDiffWireDiffsBothRequestAndResponseHeadersWithCorrectScope(t *testing.T) {
	// W11: label request headers with scope "resp". W12: drop response
	// headers entirely.
	a := []trace.Hop{{Seq: 1, Method: "GET", Path: "/x", Status: 200,
		Req:  trace.Payload{Headers: map[string]string{"x-req": "a"}},
		Resp: trace.Payload{Headers: map[string]string{"x-resp": "a"}},
	}}
	b := []trace.Hop{{Seq: 1, Method: "GET", Path: "/x", Status: 200,
		Req:  trace.Payload{Headers: map[string]string{"x-req": "b"}},
		Resp: trace.Payload{Headers: map[string]string{"x-resp": "b"}},
	}}
	w := DiffWire(a, b, Options{})
	e := w.Paired[0]
	byName := map[string]HeaderDiff{}
	for _, hd := range e.HeaderDiff {
		byName[hd.Name] = hd
	}
	reqHD, ok := byName["x-req"]
	if !ok || reqHD.Scope != "req" {
		t.Fatalf("HeaderDiff = %+v, want x-req with Scope=req (W11)", e.HeaderDiff)
	}
	respHD, ok := byName["x-resp"]
	if !ok || respHD.Scope != "resp" {
		t.Fatalf("HeaderDiff = %+v, want x-resp with Scope=resp (W12)", e.HeaderDiff)
	}
}

func TestDiffWireEmitsStatusChange(t *testing.T) {
	// W13: buildEntry never emits StatusChange.
	a := []trace.Hop{hop(1, "GET", "/x", 200, "", "")}
	b := []trace.Hop{hop(1, "GET", "/x", 500, "", "")}
	w := DiffWire(a, b, Options{})
	e := w.Paired[0]
	if e.StatusChange == nil || e.StatusChange.A != 200 || e.StatusChange.B != 500 {
		t.Fatalf("StatusChange = %+v, want A=200 B=500 (W13)", e.StatusChange)
	}
}

func TestDiffWireAnnotatesPairedEntriesWithClassesAndPositions(t *testing.T) {
	// W6: delete the annotate(entries) call — Moved/PosA/PosB/Classes would
	// all stay at their zero values.
	a := []trace.Hop{
		hop(1, "GET", "/a", 200, "", ""),
		hop(2, "GET", "/b", 200, "", ""),
		hop(3, "GET", "/c", 200, "", ""),
	}
	b := []trace.Hop{
		hop(10, "GET", "/c", 200, "", ""),
		hop(11, "GET", "/a", 200, "", ""),
		hop(12, "GET", "/b", 200, "", ""),
	}
	w := DiffWire(a, b, Options{})
	if len(w.Paired) != 3 {
		t.Fatalf("Paired = %d, want 3", len(w.Paired))
	}
	for _, e := range w.Paired {
		if len(e.Classes) == 0 {
			t.Fatalf("entry %+v has no Classes — annotate must have run (W6)", e)
		}
	}
	var moved int
	for _, e := range w.Paired {
		if e.Moved {
			moved++
		}
	}
	if moved == 0 {
		t.Fatalf("Paired = %+v, want at least one Moved entry (W6)", w.Paired)
	}
}

func TestATruncatedBodyIsFlaggedAndFallsBackToAWholeBodyDiff(t *testing.T) {
	// Truncated is an OR of four independent flags (A/B × req/resp) — each
	// one gets its own case so a mutation that drops or duplicates any
	// single term is observable, not just "truncated on A.Resp".
	//
	// F1/F5 ruling: truncation gates PER PAYLOAD, not per entry. buildEntry
	// no longer skips diffing when e.Truncated — parseBody's own per-payload
	// Truncated guard (previously dead code, see M16) makes the truncated
	// side fall back to the SAME whole-string comparison used for a
	// non-JSON body: one opaque FieldDiff at Path "", never a field tree
	// over half-parsed data. The untouched scope (req when resp is
	// truncated, and vice versa) is zero-value on both sides in these
	// fixtures, so it produces no diff of its own.
	cases := []struct {
		name  string
		p     Pair
		scope string
	}{
		{"A.Resp", Pair{A: trace.Hop{Resp: trace.Payload{Body: `{"a":1}`, Truncated: true}}, B: trace.Hop{Resp: trace.Payload{Body: `{"a":2}`}}}, "resp"},
		{"B.Resp", Pair{A: trace.Hop{Resp: trace.Payload{Body: `{"a":1}`}}, B: trace.Hop{Resp: trace.Payload{Body: `{"a":2}`, Truncated: true}}}, "resp"},
		{"A.Req", Pair{A: trace.Hop{Req: trace.Payload{Body: `{"a":1}`, Truncated: true}}, B: trace.Hop{Req: trace.Payload{Body: `{"a":2}`}}}, "req"},
		{"B.Req", Pair{A: trace.Hop{Req: trace.Payload{Body: `{"a":1}`}}, B: trace.Hop{Req: trace.Payload{Body: `{"a":2}`, Truncated: true}}}, "req"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.p.Method, c.p.NormalizedPath = "POST", "/upload"
			e := buildEntry(c.p, rules.Resolved{}, Options{})
			if !e.Truncated {
				t.Fatalf("Truncated = false, want true")
			}
			if len(e.BodyTolerated) != 0 || len(e.BodyViolations) != 0 || len(e.OrderingChanges) != 0 {
				t.Fatalf("a truncated body must never produce a rule outcome: %+v", e)
			}
			if len(e.BodyDiff) != 1 || e.BodyDiff[0].Path != "" || e.BodyDiff[0].Scope != c.scope {
				t.Fatalf("BodyDiff = %+v, want exactly one whole-body diff in scope %s at Path \"\" — never a field tree over a truncated body", e.BodyDiff, c.scope)
			}
		})
	}
}

// TestATruncatedBodyStillFallsBackEvenWhenSyntacticallyValidJSON pins M16:
// parseBody must gate on p.Truncated BEFORE attempting to parse, not rely
// on the cut body happening to be invalid JSON. A body that is truncated
// but coincidentally still parses must still be denied a field walk.
func TestATruncatedBodyStillFallsBackEvenWhenSyntacticallyValidJSON(t *testing.T) {
	a := trace.Payload{Body: `{"a":1,"b":2}`, Truncated: true}
	b := trace.Payload{Body: `{"a":1,"b":3}`}
	acc := &bodyAcc{}
	diffBodyScope("resp", a, b, diffCtx{}, acc)
	if len(acc.Tolerated)+len(acc.Violations)+len(acc.Ignored) != 0 {
		t.Fatalf("acc = %+v, want no rule outcomes from a suppressed field walk", acc)
	}
	if len(acc.Diff) != 1 || acc.Diff[0].Path != "" {
		t.Fatalf("acc.Diff = %+v, want exactly one whole-body diff at Path \"\", never a field tree over a truncated body", acc.Diff)
	}
}

func TestNilDeviationsToleratesNothing(t *testing.T) {
	a := []trace.Hop{hop(1, "GET", "/only-a", 200, "", ""), hop(2, "GET", "/x", 200, "", "")}
	b := []trace.Hop{hop(1, "GET", "/only-b", 200, "", ""), hop(2, "GET", "/x", 200, "", "")}
	// The ZERO VALUE of Options.Deviations must be the refusing one: an
	// empty ledger tolerates nothing, so a diff that forgot to load one
	// reports every difference rather than silently sanctioning it. Task 11
	// added the applying half (see deviations_test.go's
	// TestASanctionedDeviationAnnotatesButDoesNotHide); this pins that the
	// absence of a ledger never becomes a tolerance.
	for _, deviations := range [][]Deviation{nil, {}} {
		w := DiffWire(a, b, Options{Deviations: deviations})
		for _, c := range append(append([]Call{}, w.Missing...), w.Extra...) {
			if c.Tolerated != nil {
				t.Fatalf("Call.Tolerated = %+v, want nil — an empty ledger must tolerate nothing", c.Tolerated)
			}
		}
	}
}

// --- wire JSON key contract ---

func jsonKeys(t *testing.T, v any) []string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("Marshal(%T): %v", v, err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("value is not a JSON object: %v (got %s)", err, b)
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func assertJSONKeys(t *testing.T, v any, want []string) {
	t.Helper()
	got := jsonKeys(t, v)
	wantSorted := append([]string(nil), want...)
	sort.Strings(wantSorted)
	if !reflect.DeepEqual(got, wantSorted) {
		t.Fatalf("%T JSON keys = %v, want %v", v, got, wantSorted)
	}
}

// TestWireJSONKeysMatchContract pins the on-disk/wire key name of every
// exported field on every wire type this task declares. Every optional
// field is populated so omitempty can't hide a rename by omitting the key.
func TestWireJSONKeysMatchContract(t *testing.T) {
	fd := FieldDiff{Scope: "resp", Path: "id", Type: "changed", A: 1, B: 2, Matcher: "uuid", Glob: "id"}
	assertJSONKeys(t, fd, []string{"scope", "path", "type", "a", "b", "matcher", "glob"})

	hd := HeaderDiff{Scope: "resp", Name: "etag", Type: "changed", A: "1", B: "2", Matcher: "etag"}
	assertJSONKeys(t, hd, []string{"scope", "name", "type", "a", "b", "matcher"})

	sc := StatusChange{A: 200, B: 500}
	assertJSONKeys(t, sc, []string{"a", "b"})

	full := Entry{
		Method: "GET", NormalizedPath: "/x", SeqA: 1, SeqB: 2, PosA: 0, PosB: 0,
		GroupA: "browse", GroupB: "browse", Moved: true, Truncated: true,
		Classes:         []string{"changed"},
		StatusChange:    &sc,
		BodyDiff:        []FieldDiff{fd},
		BodyTolerated:   []FieldDiff{fd},
		BodyViolations:  []FieldDiff{fd},
		BodyIgnored:     []FieldDiff{fd},
		OrderingChanges: []FieldDiff{fd},
		HeaderDiff:      []HeaderDiff{hd},
	}
	assertJSONKeys(t, full, []string{
		"method", "normalizedPath", "seqA", "seqB", "posA", "posB", "groupA", "groupB",
		"moved", "truncated", "classes", "statusChange", "bodyDiff", "bodyTolerated",
		"bodyViolations", "bodyIgnored", "orderingChanges", "headerDiff",
	})

	// The `full` Entry above cannot detect an omitempty on any of these
	// fields — every one of them is populated, so the tag never fires. This
	// is the shape that matters and the one that was uncovered: an
	// UNCHANGED paired call, which is the most common row in any summary
	// and the one where all seven array fields are empty.
	//
	// Array fields carry no omitempty, so a consumer reads the same key set
	// from every Entry it is handed. Without that, entry.bodyDiff.map(...)
	// throws on the commonest row the review UI renders.
	//
	// "moved" and "truncated" are in this list, and they moved here in Task
	// 15's fix round (D2): they used to be omitempty, so a FALSE bool was an
	// absent key on exactly this row — the unchanged paired call. Their
	// presence-ness is now the same as every other field's, and this
	// assertion is the golden that pins it: put omitempty back and this line
	// goes red.
	bare := Entry{Method: "GET", NormalizedPath: "/x", SeqA: 1, SeqB: 1}
	ensureEntryArrays(&bare)
	assertJSONKeys(t, bare, []string{
		"method", "normalizedPath", "seqA", "seqB", "posA", "posB",
		"moved", "truncated", "classes", "bodyDiff", "bodyTolerated",
		"bodyViolations", "bodyIgnored", "orderingChanges", "headerDiff",
	})

	note := ToleratedNote{ID: "d1", Reason: "expected"}
	assertJSONKeys(t, note, []string{"id", "reason"})

	call := Call{Method: "GET", Path: "/x", Seq: 1, Status: 200, Group: "browse", Tolerated: &note}
	assertJSONKeys(t, call, []string{"method", "path", "seq", "status", "group", "tolerated"})

	gn := GroupNames{A: []string{"browse"}, B: []string{"browse"}}
	assertJSONKeys(t, gn, []string{"a", "b"})

	wire := Wire{Paired: []Entry{full}, Missing: []Call{call}, Extra: []Call{call}, Groups: &gn}
	assertJSONKeys(t, wire, []string{"paired", "missing", "extra", "groups"})

	sec := Section{Name: "browse", Entries: []Entry{full}, Counts: map[string]int{"changed": 1}}
	assertJSONKeys(t, sec, []string{"name", "entries", "counts"})

	dev := Deviation{ID: "d1", Status: "approved", Apps: [2]string{"a", "b"}, Method: "GET", Path: "/x", Reason: "expected"}
	assertJSONKeys(t, dev, []string{"id", "status", "apps", "method", "path", "reason"})
}
