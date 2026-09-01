package rules

import (
	"testing"

	"github.com/caribou-crew/ensemble/core/trace"
)

func TestNamedMatchersAcceptTheirFormatAndRejectOthers(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  bool
	}{
		{"uuid", "3f2504e0-4f89-11d3-9a0c-0305e82c3301", true},
		{"uuid", "not-a-uuid", false},
		{"iso8601", "2026-08-21T10:15:00.123Z", true},
		{"iso8601", "Wed", false}, // stricter than time.Parse-anything on purpose
		{"http-date", "Wed, 21 Aug 2026 10:15:00 GMT", true},
		{"http-date", "2026-08-21T10:15:00Z", false},
		// Matches httpDateRe's shape but August has only 31 days — the
		// parses() backstop must still catch it.
		{"http-date", "Wed, 32 Aug 2026 10:15:00 GMT", false},
		{"etag", `W/"abc"`, true},
		{"etag", "abc", false},
		{"integer", 1760.0, true}, // a JSON number decodes as float64
		{"integer", "1760", true}, // a header carries the string form
		{"integer", 17.6, false},
		{"semver", "1.2.3-rc.1", true},
		{"semver", "1.2", false},
	}
	for _, c := range cases {
		m, err := ParseMatcher(c.name, "test")
		if err != nil {
			t.Fatalf("ParseMatcher(%q): %v", c.name, err)
		}
		got := Classify(m, c.value, c.value, true) == Tolerated
		if got != c.want {
			t.Errorf("%s(%v) satisfied = %v, want %v", c.name, c.value, got, c.want)
		}
	}
}

// TestIntegerAcceptsGoNumericKindsNotJustJSONFloat64 covers m5: every value
// reaching Classify from encoding/json arrives as float64, but a Go-side
// caller may hand this int/int64/uint/float32 directly — accept the common
// Go integer kinds rather than silently failing an obviously-integer value.
func TestIntegerAcceptsGoNumericKindsNotJustJSONFloat64(t *testing.T) {
	m, err := ParseMatcher("integer", "test")
	if err != nil {
		t.Fatalf("ParseMatcher: %v", err)
	}
	for _, v := range []any{int(7), int64(7), uint(7), float32(7)} {
		if got := Classify(m, v, v, true); got != Tolerated {
			t.Errorf("integer(%T %v) = %v, want Tolerated", v, v, got)
		}
	}
	if got := Classify(m, float32(17.6), float32(17.6), true); got != Violation {
		t.Errorf("integer(float32 17.6) = %v, want Violation", got)
	}
}

func TestMatcherToleratesValueChangeButCatchesShapeChange(t *testing.T) {
	m, _ := ParseMatcher("uuid", "test")
	if got := Classify(m, "3f2504e0-4f89-11d3-9a0c-0305e82c3301", "6ec0bd7f-11c0-43da-975e-2a8ad9ebae0b", true); got != Tolerated {
		t.Errorf("two uuids = %v, want tolerated", got)
	}
	if got := Classify(m, "3f2504e0-4f89-11d3-9a0c-0305e82c3301", 42.0, true); got != Violation {
		t.Errorf("uuid vs number = %v, want violation", got)
	}
}

// TestIso8601AcceptsEveryShapeItsOwnRegexBlesses pins M1: the parses()
// backstop must accept every shape isoRe's grammar blesses, including the
// colon-less zone offset ("+0530") that Java, Python and Go's own
// time.Format("...Z0700") all emit. A previous version narrowed past the
// regex and reported "violation" on exactly the timestamps a user reached
// for iso8601 to excuse. It must also still catch a value the regex
// matches but that isn't a real date (the backstop's whole job).
func TestIso8601AcceptsEveryShapeItsOwnRegexBlesses(t *testing.T) {
	m, err := ParseMatcher("iso8601", "test")
	if err != nil {
		t.Fatalf("ParseMatcher: %v", err)
	}
	accepted := []string{
		"2026-08-21T10:15:00.123Z",
		"2026-08-13T10:20:24.149439-05:00",
		"2026-08-21T10:15:00+0530",     // colon-less offset — Java/Python/Go emit this
		"2026-08-21T10:15:00-0500",     // colon-less offset, negative
		"2026-08-21 10:15:00Z",         // space separator instead of T
		"2026-08-21T10:15:00",          // no zone at all — isoRe's zone group is optional
		"2026-08-21 10:15:00.123Z",     // fractional seconds with the space separator
		"2026-08-21T10:15:00.123+0530", // fractional seconds with a colon-less offset
	}
	for _, v := range accepted {
		if got := Classify(m, v, v, true); got != Tolerated {
			t.Errorf("iso8601 rejected %q, want Tolerated (got %v)", v, got)
		}
	}
	// Matches isoRe's shape but is not a real calendar date — the backstop
	// must still catch it, or the regex-only gate is all that's left.
	if got := Classify(m, "2026-08-21T10:15:00.123Z", "2026-13-45T99:99:99Z", true); got != Violation {
		t.Errorf("iso8601 accepted a shape-only match with an impossible date, got %v", got)
	}
}

func TestUnknownMatcherNameIsAnErrorNotSilentTolerance(t *testing.T) {
	if _, err := ParseMatcher("uuidv4", "wireRules[0].body.id"); err == nil {
		t.Fatal("want an error naming the location and the valid names")
	}
}

func TestCustomPatternRequiresBothSidesToMatch(t *testing.T) {
	m, err := ParseMatcher(map[string]any{"pattern": `^v\d+$`}, "test")
	if err != nil {
		t.Fatalf("ParseMatcher: %v", err)
	}
	if got := Classify(m, "v1", "v2", true); got != Tolerated {
		t.Errorf("both match = %v, want tolerated", got)
	}
	if got := Classify(m, "v1", "x", true); got != Violation {
		t.Errorf("one side fails = %v, want violation", got)
	}
	if m.Label() != `/^v\d+$/` {
		t.Errorf("Label = %q", m.Label())
	}
}

// TestZeroMatcherAlwaysMeansChanged pins C1: the zero Matcher ("no rule
// applies") must classify as Changed in every arity — present on both
// sides, present on one, absent on both — never Ignored, never Violation.
// A previous version of this suite left this entirely unpinned: mutating
// the zero-Matcher branch to Ignored ("no rule means fine") or to Violation
// both left the suite green. Under the Ignored mutation, Task 8's DiffWire
// would classify every unruled body field as ignored and `retrace diff`
// would exit 0 on a run where every field changed — the single worst
// outcome this product can have.
func TestZeroMatcherAlwaysMeansChanged(t *testing.T) {
	var zero Matcher
	if !zero.Zero() {
		t.Fatal("the Go zero value of Matcher must report Zero() == true")
	}
	if got := Classify(zero, "a", "b", true); got != Changed {
		t.Errorf("zero matcher, both present = %v, want Changed", got)
	}
	if got := Classify(zero, "a", nil, false); got != Changed {
		t.Errorf("zero matcher, one-sided = %v, want Changed", got)
	}
	if got := Classify(zero, nil, nil, false); got != Changed {
		t.Errorf("zero matcher, absent on both = %v, want Changed", got)
	}
}

// TestClassifyIsTotalOnAMatcherParseMatcherNeverBuilt pins M2: Classify must
// never panic, including on a Matcher that arrived by a route other than
// ParseMatcher — most importantly a JSON round-trip of a valid pattern
// matcher, since Matcher.re is unexported and does not survive
// serialization. A structurally invalid matcher must fail closed
// (Violation), never Ignored or Tolerated.
func TestClassifyIsTotalOnAMatcherParseMatcherNeverBuilt(t *testing.T) {
	// A JSON round-trip of a valid pattern matcher: re is lost, Pattern
	// survives. Classify must recompile it and keep evaluating correctly.
	original, err := ParseMatcher(map[string]any{"pattern": `^v\d+$`}, "test")
	if err != nil {
		t.Fatalf("ParseMatcher: %v", err)
	}
	roundTripped := Matcher{Kind: original.Kind, Pattern: original.Pattern}
	if got := Classify(roundTripped, "v1", "v2", true); got != Tolerated {
		t.Errorf("round-tripped pattern matcher, both match = %v, want Tolerated", got)
	}
	if got := Classify(roundTripped, "v1", "x", true); got != Violation {
		t.Errorf("round-tripped pattern matcher, one fails = %v, want Violation", got)
	}

	// Cannot be evaluated: unknown named matcher, empty pattern, and an
	// unrecognized Kind must all fail closed rather than panic.
	cases := []struct {
		name string
		m    Matcher
	}{
		{"unknown named matcher", Matcher{Kind: KindNamed, Name: "uuidv4"}},
		{"named matcher with no name", Matcher{Kind: KindNamed}},
		{"pattern matcher with no pattern and no compiled re", Matcher{Kind: KindPattern}},
		{"pattern matcher with an uncompilable pattern and no compiled re", Matcher{Kind: KindPattern, Pattern: "["}},
		{"unrecognized kind", Matcher{Kind: "bogus"}},
	}
	for _, c := range cases {
		if got := Classify(c.m, "x", "x", true); got != Violation {
			t.Errorf("%s: Classify = %v, want Violation (fail closed)", c.name, got)
		}
	}
}

// TestPatternMatchersAreNotAutoAnchored pins m2: a {pattern: ...} matcher
// compiles the pattern as-is, exactly like the prototype's `new RegExp(p)` — it
// is not wrapped in `^(?:...)$`. So an unanchored pattern tolerates a
// substring match; anchoring, if wanted, is the caller's job. This is a
// deliberate tolerance-widening default, not an oversight.
func TestPatternMatchersAreNotAutoAnchored(t *testing.T) {
	m, err := ParseMatcher(map[string]any{"pattern": `v\d+`}, "test")
	if err != nil {
		t.Fatalf("ParseMatcher: %v", err)
	}
	if got := Classify(m, "xxv1yy", "zzv9zz", true); got != Tolerated {
		t.Errorf("unanchored pattern as a substring match = %v, want Tolerated", got)
	}
}

// TestEmptyPatternIsRejected pins m3: this is deliberate hardening over
// the prototype, which accepts {pattern: ""} and compiles new RegExp(”) — a
// regex that matches everything, i.e. total silent tolerance for that
// field. Rejecting it at parse time is a genuine improvement; pin it so a
// future "port fidelity" pass doesn't undo it.
func TestEmptyPatternIsRejected(t *testing.T) {
	if _, err := ParseMatcher(map[string]any{"pattern": ""}, "wireRules[0].body.id"); err == nil {
		t.Fatal("want an error rejecting an empty pattern, not silent total tolerance")
	}
}

func TestAValueMatcherNeverExcusesAnAppearingOrDisappearingField(t *testing.T) {
	uuid, _ := ParseMatcher("uuid", "test")
	if got := Classify(uuid, "3f2504e0-4f89-11d3-9a0c-0305e82c3301", nil, false); got != Changed {
		t.Errorf("one-sided value under a matcher = %v, want changed", got)
	}
	ign, _ := ParseMatcher("ignore", "test")
	if got := Classify(ign, "x", nil, false); got != Ignored {
		t.Errorf("ignore must silence a one-sided value, got %v", got)
	}
}

// TestRedactedMatcherIsAsymmetric pins the one matcher whose two sides are
// NOT interchangeable: a recorded destroy-sentinel tolerates ANY live value
// ("something secret was here" asserts shape, not content), but a recorded
// value that is not the sentinel — including the live side carrying it while
// the recorded side does not — is a Violation like any other unsatisfied
// named matcher.
func TestRedactedMatcherIsAsymmetric(t *testing.T) {
	m, err := ParseMatcher("redacted", "test")
	if err != nil {
		t.Fatalf("ParseMatcher: %v", err)
	}
	if got := Classify(m, trace.Redacted, "hunter2", true); got != Tolerated {
		t.Errorf("recorded sentinel vs live secret = %v, want Tolerated", got)
	}
	if got := Classify(m, trace.Redacted, 42.0, true); got != Tolerated {
		t.Errorf("recorded sentinel vs live non-string = %v, want Tolerated", got)
	}
	if got := Classify(m, "plaintext", "hunter2", true); got != Violation {
		t.Errorf("recorded non-sentinel = %v, want Violation", got)
	}
	if got := Classify(m, "hunter2", trace.Redacted, true); got != Violation {
		t.Errorf("sentinel on the LIVE side only = %v, want Violation", got)
	}
	// One-sided: the general rule holds — a value matcher never excuses an
	// appearing or disappearing field.
	if got := Classify(m, trace.Redacted, nil, false); got != Changed {
		t.Errorf("one-sided redacted = %v, want Changed", got)
	}
}
