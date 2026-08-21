package diff

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/retrace/rules"
)

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
	// A and B use NON-OVERLAPPING Seq ranges (101-103 vs 201-202) — CallSimilarity
	// never looks at Seq, so this doesn't perturb the algorithm under test,
	// but it means a Pair.A/Pair.B field swap is independently observable
	// even though the matched bodies read the same on both sides.
	a := []trace.Hop{
		hop(101, "POST", "/cart", 200, "X", ""),
		hop(102, "POST", "/cart", 200, "Y", ""),
		hop(103, "POST", "/cart", 200, "Z", ""),
	}
	b := []trace.Hop{
		hop(201, "POST", "/cart", 200, "Y", ""),
		hop(202, "POST", "/cart", 200, "Z", ""),
	}
	pairs, missing, extra := PairCalls(a, b, nil)
	if len(pairs) != 2 {
		t.Fatalf("len(pairs) = %d, want 2", len(pairs))
	}
	for _, p := range pairs {
		if p.A.Req.Body != p.B.Req.Body {
			t.Fatalf("pair mismatched bodies: A=%q B=%q, want same-letter pairing", p.A.Req.Body, p.B.Req.Body)
		}
		if p.A.Seq < 101 || p.A.Seq > 103 {
			t.Fatalf("p.A.Seq = %d, want a side-a seq (101-103) — Pair.A must hold the side-a hop", p.A.Seq)
		}
		if p.B.Seq < 201 || p.B.Seq > 202 {
			t.Fatalf("p.B.Seq = %d, want a side-b seq (201-202) — Pair.B must hold the side-b hop", p.B.Seq)
		}
	}
	if len(missing) != 1 || missing[0].Req.Body != "X" {
		t.Fatalf("missing = %+v, want [X]", missing)
	}
	if len(extra) != 0 {
		t.Fatalf("extra = %+v, want none", extra)
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

func TestATruncatedBodyIsFlaggedAndNotFieldDiffed(t *testing.T) {
	// Truncated is an OR of four independent flags (A/B × req/resp) — each
	// one gets its own case so a mutation that drops or duplicates any
	// single term is observable, not just "truncated on A.Resp".
	cases := []struct {
		name string
		p    Pair
	}{
		{"A.Resp", Pair{A: trace.Hop{Resp: trace.Payload{Body: `{"a":1}`, Truncated: true}}, B: trace.Hop{Resp: trace.Payload{Body: `{"a":2}`}}}},
		{"B.Resp", Pair{A: trace.Hop{Resp: trace.Payload{Body: `{"a":1}`}}, B: trace.Hop{Resp: trace.Payload{Body: `{"a":2}`, Truncated: true}}}},
		{"A.Req", Pair{A: trace.Hop{Req: trace.Payload{Body: `{"a":1}`, Truncated: true}}, B: trace.Hop{Req: trace.Payload{Body: `{"a":2}`}}}},
		{"B.Req", Pair{A: trace.Hop{Req: trace.Payload{Body: `{"a":1}`}}, B: trace.Hop{Req: trace.Payload{Body: `{"a":2}`, Truncated: true}}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.p.Method, c.p.NormalizedPath = "POST", "/upload"
			e := buildEntry(c.p, rules.Resolved{}, Options{})
			if !e.Truncated {
				t.Fatalf("Truncated = false, want true")
			}
			if len(e.BodyDiff) != 0 || len(e.BodyTolerated) != 0 || len(e.BodyViolations) != 0 || len(e.OrderingChanges) != 0 {
				t.Fatalf("a truncated body must never be field-diffed: %+v", e)
			}
		})
	}
}

func TestNilDeviationsToleratesNothing(t *testing.T) {
	a := []trace.Hop{hop(1, "GET", "/only-a", 200, "", ""), hop(2, "GET", "/x", 200, "", "")}
	b := []trace.Hop{hop(1, "GET", "/only-b", 200, "", ""), hop(2, "GET", "/x", 200, "", "")}
	// A populated Deviations list is still a no-op in Task 8 — Task 11 owns
	// applying the ledger. This must hold whether Deviations is nil or set.
	dev := []Deviation{{ID: "d1", Status: "approved", Apps: [2]string{"a", "b"}, Method: "GET", Path: "/only-a", Reason: "expected"}}
	for _, deviations := range [][]Deviation{nil, dev} {
		w := DiffWire(a, b, Options{Deviations: deviations})
		for _, c := range append(append([]Call{}, w.Missing...), w.Extra...) {
			if c.Tolerated != nil {
				t.Fatalf("Call.Tolerated = %+v, want nil — Task 8 must not consume Options.Deviations", c.Tolerated)
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
