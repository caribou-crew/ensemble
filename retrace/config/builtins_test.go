package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caribou-crew/ensemble/retrace/rules"
)

// resolveHeaders loads a retrace.yaml from src and resolves the rules for
// one call, which is the only thing every test below cares about.
func resolveHeaders(t *testing.T, src string) rules.Resolved {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "retrace.yaml"), []byte(src), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := Load(filepath.Join(dir, "retrace.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	rs, err := cfg.Rules()
	if err != nil {
		t.Fatalf("Rules: %v", err)
	}
	return rules.Resolve(rs, "GET", "/products")
}

func TestEveryBuiltinHeaderResolvesWithNoConfigAtAll(t *testing.T) {
	// The headline case: a retrace.yaml that says nothing about headers
	// still gets all three. Asserted per header with the matcher NAME, not
	// merely "something is set" — swapping http-date for ignore, or
	// dropping one entry from the map, has to turn this red.
	want := map[string]string{
		"date":           "http-date",
		"etag":           "etag",
		"content-length": "integer",
	}
	res := resolveHeaders(t, "app: web\n")
	for name, matcher := range want {
		if got := res.ForHeader(name).Name; got != matcher {
			t.Errorf("ForHeader(%q).Name = %q, want %q", name, got, matcher)
		}
	}
	// The set is exactly these three. A fourth built-in added without a
	// deliberate edit here is a tolerance nobody reviewed.
	if got := len(BuiltinHeaderNames()); got != len(want) {
		t.Errorf("BuiltinHeaderNames() has %d entries, want %d: %v", got, len(want), BuiltinHeaderNames())
	}
}

func TestABuiltinAppliesToAConfigThatWasNeverLoadedFromDisk(t *testing.T) {
	// Discover with no retrace.yaml hands back a defaulted Config, and
	// `retrace run --no-config` proceeds on one. Both must still tolerate a
	// clock — the built-ins are not a reward for having written a config.
	cfg, err := Discover(t.TempDir())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	rs, err := cfg.Rules()
	if err != nil {
		t.Fatalf("Rules: %v", err)
	}
	if got := rules.Resolve(rs, "GET", "/x").ForHeader("date").Name; got != "http-date" {
		t.Errorf(`no-config date matcher = %q, want "http-date"`, got)
	}
}

func TestAUserRuleForABuiltinHeaderWinsAndLeavesTheOthersAlone(t *testing.T) {
	res := resolveHeaders(t, "app: web\nwire_rules:\n  - headers: { date: iso8601 }\n")
	if got := res.ForHeader("date").Name; got != "iso8601" {
		t.Errorf(`user rule must beat the built-in: date = %q, want "iso8601"`, got)
	}
	// The other two are the control. A mutant that drops the built-ins
	// entirely the moment the user writes any wire_rule would satisfy the
	// assertion above and fail here.
	if got := res.ForHeader("etag").Name; got != "etag" {
		t.Errorf(`overriding date must not disturb etag: got %q, want "etag"`, got)
	}
	if got := res.ForHeader("content-length").Name; got != "integer" {
		t.Errorf(`overriding date must not disturb content-length: got %q, want "integer"`, got)
	}
}

func TestExactOnABuiltinHeaderRestoresStrictComparison(t *testing.T) {
	// The documented per-header escape hatch. `exact` is not "no rule" —
	// it is a rule that classifies any difference as Changed, which is what
	// makes it able to beat a built-in at all.
	res := resolveHeaders(t, "app: web\nwire_rules:\n  - headers: { date: exact }\n")
	m := res.ForHeader("date")
	if m.Kind != rules.KindExact {
		t.Fatalf("date matcher kind = %q, want %q", m.Kind, rules.KindExact)
	}
	if got := rules.Classify(m, "Mon, 01 Jan 2024 00:00:00 GMT", "Tue, 02 Jan 2024 00:00:00 GMT", true); got != rules.Changed {
		t.Errorf("two different dates under `exact` classify as %q, want %q", got, rules.Changed)
	}
}

func TestDefaultWireRulesFalseTurnsEveryBuiltinOff(t *testing.T) {
	res := resolveHeaders(t, "app: web\ndefault_wire_rules: false\n")
	for _, name := range BuiltinHeaderNames() {
		if m := res.ForHeader(name); !m.Zero() {
			t.Errorf("default_wire_rules: false must leave %q unruled, got %+v", name, m)
		}
	}
}

func TestDefaultWireRulesTrueIsTheSameAsOmittingIt(t *testing.T) {
	// Pins that the *bool reads its VALUE and not merely its presence. A
	// mutant returning `c.DefaultWireRules == nil` — off whenever the key
	// appears at all — passes the false case above and fails here.
	res := resolveHeaders(t, "app: web\ndefault_wire_rules: true\n")
	if got := res.ForHeader("date").Name; got != "http-date" {
		t.Errorf(`default_wire_rules: true date = %q, want "http-date"`, got)
	}
}

func TestBuiltinsTolerateTwoValidDatesButNotAMissingHeader(t *testing.T) {
	// Why these are named matchers rather than `ignore`, stated as a test.
	// Swapping http-date for ignore in builtinHeaderMatchers passes the
	// first assertion and fails the second.
	m := resolveHeaders(t, "app: web\n").ForHeader("date")
	if got := rules.Classify(m, "Mon, 01 Jan 2024 00:00:00 GMT", "Tue, 02 Jan 2024 00:00:00 GMT", true); got != rules.Tolerated {
		t.Errorf("two valid HTTP-dates classify as %q, want %q", got, rules.Tolerated)
	}
	if got := rules.Classify(m, "Mon, 01 Jan 2024 00:00:00 GMT", nil, false); got != rules.Changed {
		t.Errorf("a date present on one side only classifies as %q, want %q", got, rules.Changed)
	}
	if got := rules.Classify(m, "Mon, 01 Jan 2024 00:00:00 GMT", "not-a-date", true); got != rules.Violation {
		t.Errorf("a malformed date classifies as %q, want %q", got, rules.Violation)
	}
}

func TestRulesIsIdempotentAcrossRepeatedCalls(t *testing.T) {
	// Load validates by calling Rules, Discover calls it again after
	// merging the overlay, and consumers call it once more. A version that
	// appended the built-ins to c.WireRules instead of prepending to a
	// local would grow the list by three every time — invisible in
	// behaviour, since the extra copies resolve identically, until the
	// serve UI renders the user's rule list.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "retrace.yaml"), []byte("app: web\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := Load(filepath.Join(dir, "retrace.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	first, err := cfg.Rules()
	if err != nil {
		t.Fatalf("Rules: %v", err)
	}
	if _, err := cfg.Rules(); err != nil {
		t.Fatalf("Rules (second): %v", err)
	}
	second, err := cfg.Rules()
	if err != nil {
		t.Fatalf("Rules (third): %v", err)
	}
	if len(first) != len(second) {
		t.Errorf("Rules() grew across calls: %d then %d", len(first), len(second))
	}
	// The user's own list is what the serve UI renders; it must never have
	// acquired the built-ins as a side effect of resolving them.
	if len(cfg.WireRules) != 0 {
		t.Errorf("Rules() wrote the built-ins into WireRules: %+v", cfg.WireRules)
	}
}

func TestABadMatcherIsReportedAtTheUsersOwnRuleIndex(t *testing.T) {
	// rules.Normalize labels errors by index into the slice it is handed.
	// Concatenating the built-ins in front of the user's rules renumbered
	// all of them, so the user's rule 0 reported as `wireRules[3]` —
	// an index pointing at a rule they never wrote. Caught by
	// TestAnInvalidMatcherFailsLoadNamingTheRule; pinned here because that
	// test only happens to use the first rule.
	dir := t.TempDir()
	src := "app: web\nwire_rules:\n  - headers: { x-a: uuid }\n  - headers: { x-b: nonsense }\n"
	if err := os.WriteFile(filepath.Join(dir, "retrace.yaml"), []byte(src), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	_, err := Load(filepath.Join(dir, "retrace.yaml"))
	if err == nil {
		t.Fatal("an unknown matcher must fail Load")
	}
	if !strings.Contains(err.Error(), "wireRules[1]") {
		t.Errorf("error must name the user's own rule index, got: %v", err)
	}
}

func TestBuiltinWireRulesHandsBackIndependentCopies(t *testing.T) {
	// Prepending the built-ins to a caller's slice must not let one
	// caller's mutation reach the next one's rules.
	a, b := BuiltinWireRules(), BuiltinWireRules()
	if len(a) == 0 {
		t.Fatal("BuiltinWireRules returned nothing")
	}
	for name := range a[0].Headers {
		a[0].Headers[name] = "uuid"
	}
	for name, matcher := range b[0].Headers {
		if matcher == "uuid" {
			t.Fatalf("mutating one BuiltinWireRules result changed another: %q", name)
		}
	}
}

func TestBuiltinRuleOrderIsStable(t *testing.T) {
	// Map iteration is random. Without the sort this fails within a few
	// runs, and the failure it would eventually cause — a rule ordering
	// that differs between two processes reading the same config — is far
	// harder to see than this.
	first := BuiltinWireRules()
	for i := 0; i < 20; i++ {
		next := BuiltinWireRules()
		for j := range first {
			for name := range first[j].Headers {
				if _, ok := next[j].Headers[name]; !ok {
					t.Fatalf("built-in rule order is unstable at index %d: %q moved", j, name)
				}
			}
		}
	}
}

func TestABuiltinHeaderCanBeGivenARuleScopedToOnePath(t *testing.T) {
	// The reason BuiltinWireRules emits one Raw per header rather than one
	// Raw carrying all three: a user rule scoped to a path must override
	// the built-in THERE and leave it standing everywhere else.
	src := "app: web\nwire_rules:\n  - path: /products\n    headers: { etag: exact }\n"
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "retrace.yaml"), []byte(src), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := Load(filepath.Join(dir, "retrace.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	rs, err := cfg.Rules()
	if err != nil {
		t.Fatalf("Rules: %v", err)
	}
	if got := rules.Resolve(rs, "GET", "/products").ForHeader("etag").Kind; got != rules.KindExact {
		t.Errorf("scoped rule must win on its own path: etag kind = %q, want %q", got, rules.KindExact)
	}
	if got := rules.Resolve(rs, "GET", "/cart").ForHeader("etag").Name; got != "etag" {
		t.Errorf(`built-in must survive on other paths: etag = %q, want "etag"`, got)
	}
}
