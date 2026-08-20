package rules

import (
	"strings"
	"testing"
)

func TestPathGlobsScopeARuleStarOneSegmentDoubleStarAnySpan(t *testing.T) {
	cases := []struct {
		glob, path string
		want       bool
	}{
		{"/experience/*", "/experience/home", true},
		{"/experience/*", "/experience/home/v2", false},
		{"/experience/*.json", "/experience/home.json", true},
		{"/api/**", "/api/v1/cart/items", true},
		{"/api/**/items", "/api/v1/cart/items", true},
		{"", "/anything", true}, // an unset path scopes to everything
	}
	for _, c := range cases {
		if got := MatchPathGlob(c.glob, c.path); got != c.want {
			t.Errorf("MatchPathGlob(%q, %q) = %v, want %v", c.glob, c.path, got, c.want)
		}
	}
}

func TestALaterMoreSpecificRuleOverridesAnEarlierGlobalOnePerKey(t *testing.T) {
	rs, err := Normalize([]Raw{
		{Headers: map[string]any{"x-request-id": "uuid", "date": "http-date"}},
		{Path: "/cart", Headers: map[string]any{"x-request-id": "ignore"}},
	})
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	res := Resolve(rs, "GET", "/cart")
	if got := res.ForHeader("X-Request-Id").Kind; got != KindIgnore {
		t.Errorf("specific rule must win for its key: %v", got)
	}
	if got := res.ForHeader("date").Name; got != "http-date" {
		t.Errorf("untouched keys keep the global rule: %v", got)
	}
}

// TestMethodScopesARuleAndIsCaseInsensitive pins M3: case-insensitivity has
// two independent halves — Normalize upper-cases the rule's stored Method,
// and Resolve upper-cases the incoming method it is matching against. A
// previous version of this test only ever fed Resolve an already-uppercase
// "POST", so deleting Resolve's strings.ToUpper(method) call left the suite
// green; only Normalize's half was covered. Both halves are exercised here.
func TestMethodScopesARuleAndIsCaseInsensitive(t *testing.T) {
	rs, err := Normalize([]Raw{
		{Method: "post", Path: "/api/v1/auth/login", Headers: map[string]any{"x-request-id": "uuid"}},
	})
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if got := Resolve(rs, "POST", "/api/v1/auth/login").ForHeader("x-request-id"); got.Zero() {
		t.Error("POST should match a rule whose method Normalize stored upper-cased as \"POST\"")
	}
	if got := Resolve(rs, "post", "/api/v1/auth/login").ForHeader("x-request-id"); got.Zero() {
		t.Error("a lower-case incoming method must still match — Resolve upper-cases it before comparing")
	}
	if got := Resolve(rs, "GET", "/api/v1/auth/login").ForHeader("x-request-id"); !got.Zero() {
		t.Error("GET must not match a POST-scoped rule")
	}
}

func TestTheLastMatchingBodyGlobWins(t *testing.T) {
	rs, _ := Normalize([]Raw{{Body: map[string]any{"**.id": "uuid"}}, {Body: map[string]any{"order.id": "integer"}}})
	res := Resolve(rs, "POST", "/orders")
	if got := res.ForField("order.id").Name; got != "integer" {
		t.Errorf("ForField = %q, want integer", got)
	}
	if got := res.ForField("user.id").Name; got != "uuid" {
		t.Errorf("ForField = %q, want uuid", got)
	}
	// the prototype's own test (test/wire-rules.test.mjs:86) asserts this third
	// case — matcherForField(resolved, 'data.other') === null — and the
	// port dropped it. A field no glob matches must resolve to the zero
	// Matcher (Zero() == true), not to some leftover matcher from the list.
	if got := res.ForField("data.other"); !got.Zero() {
		t.Errorf("ForField on an unmatched path = %+v, want the zero Matcher", got)
	}
}

// TestZeroValueResolvedLookupsPinC1 pins the other half of C1: Resolve
// itself, with no rules or no match at all, must hand back the zero Matcher
// from both ForHeader and ForField — never something that reads as "fine".
func TestZeroValueResolvedLookupsPinC1(t *testing.T) {
	res := Resolve(nil, "GET", "/x")
	if got := res.ForHeader("date"); !got.Zero() {
		t.Errorf("ForHeader with no rules at all = %+v, want the zero Matcher", got)
	}
	if got := res.ForField("a.b"); !got.Zero() {
		t.Errorf("ForField with no rules at all = %+v, want the zero Matcher", got)
	}
}

// TestSortedKeysGivesDeterministicPrecedenceWithinOneRawRule pins m1: Go map
// iteration is randomized, so header/body entries within a SINGLE Raw rule
// are sorted alphabetically before being applied — otherwise "last one
// wins" between two overlapping globs in the same Raw would be
// nondeterministic between runs of the same config. A single overlapping
// pair (as in TestTheLastMatchingBodyGlobWins, spread across two Raw
// entries) isn't enough to catch a dropped sort, because Resolve iterates
// rules in caller-supplied list order regardless — the ordering this test
// exercises is entirely about entries competing within ONE Raw. Enough
// keys are used that an unsorted map range would need to coincidentally
// re-produce alphabetical order to slip through.
func TestSortedKeysGivesDeterministicPrecedenceWithinOneRawRule(t *testing.T) {
	rs, err := Normalize([]Raw{{Body: map[string]any{
		"e.id": "uuid",
		"c.id": "uuid",
		"a.id": "uuid",
		"d.id": "uuid",
		"b.id": "uuid",
	}}})
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	got := make([]string, len(rs[0].Body))
	for i, b := range rs[0].Body {
		got[i] = b.Glob
	}
	want := []string{"a.id", "b.id", "c.id", "d.id", "e.id"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("Rule.Body order = %v, want alphabetical %v", got, want)
	}
}

// TestDoubleStarSpansZeroSegments pins m4: '**' must be able to span zero
// segments, matching the doc comment's own promise ("including none"), and
// covers two the JS prototype cases (test/wire-rules.test.mjs:56,60) the brief's
// table omitted.
func TestDoubleStarSpansZeroSegments(t *testing.T) {
	if !MatchPathGlob("/api/**", "/api") {
		t.Error("'**' must match zero segments — /api/** should match /api itself")
	}
	if !MatchPathGlob("**", "/api/v1/profile") {
		t.Error("a bare '**' must match any path")
	}
	if MatchPathGlob("/api/**", "/other/v1") {
		t.Error("'**' still scopes to its own prefix")
	}
}

func TestHeaderLookupIsCaseInsensitiveOnTheName(t *testing.T) {
	rs, err := Normalize([]Raw{{Headers: map[string]any{"X-Request-Id": "uuid"}}})
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	res := Resolve(rs, "GET", "/anything")
	if got := res.ForHeader("x-request-id"); got.Zero() {
		t.Error("a lower-case lookup should find a header stored under a different case")
	}
	if got := res.ForHeader("X-REQUEST-ID"); got.Zero() {
		t.Error("an upper-case lookup should find a header stored under a different case")
	}
}

// TestFieldGlobDoesNotFilterEmptySegments pins m6: MatchFieldGlob matches
// the prototype's matchesFieldGlob, which splits on '.' without filtering out
// empty segments — an empty segment is real structure (a JSON object can
// have an empty-string key). This deliberately differs from MatchPathGlob,
// which does filter (matching the prototype's own path splitter).
func TestFieldGlobDoesNotFilterEmptySegments(t *testing.T) {
	if MatchFieldGlob("a.b", "a..b") {
		t.Error(`"a.b" must not match "a..b" — the empty middle segment is real structure`)
	}
	if !MatchFieldGlob("a.*.b", "a..b") {
		t.Error(`"a.*.b" must match "a..b" — '*' matches any one segment, including empty`)
	}
}

// TestEmptyFieldPathDoesNotMatchTheEmptyGlob pins the empty-fieldPath guard:
// an empty fieldPath must produce zero path segments, not one empty segment
// from a naive strings.Split(fieldPath, "."). Without the guard,
// MatchFieldGlob("", "") would match — contradicting the package doc's own
// promise that MatchFieldGlob("", x) is false for every x, unlike
// MatchPathGlob("", x), because "" is a legal literal glob here, not "unset".
func TestEmptyFieldPathDoesNotMatchTheEmptyGlob(t *testing.T) {
	if MatchFieldGlob("", "") {
		t.Error(`MatchFieldGlob("", "") must be false — "" is a literal glob, not "match anything"`)
	}
}

func TestNormalizeRejectsAnUnknownMatcherNamingTheRuleIndex(t *testing.T) {
	_, err := Normalize([]Raw{
		{},
		{Headers: map[string]any{"x-request-id": "uuidv4"}},
	})
	if err == nil {
		t.Fatal("want an error naming the offending rule")
	}
	if !strings.Contains(err.Error(), "wireRules[1]") {
		t.Errorf("error should name the rule index: %v", err)
	}
}
