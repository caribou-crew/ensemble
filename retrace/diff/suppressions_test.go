package diff

import (
	"strings"
	"testing"

	"github.com/caribou-crew/ensemble/retrace/config"
	"github.com/caribou-crew/ensemble/retrace/rules"
)

// summaryWithEntries wraps entries as the one shape suppressionsOf reads.
func summaryWithEntries(entries ...Entry) Summary {
	return Summary{Wire: Wire{Paired: entries}}
}

func findSuppression(ss []Suppression, plane, target string) (Suppression, bool) {
	for _, s := range ss {
		if s.Plane == plane && s.Target == target {
			return s, true
		}
	}
	return Suppression{}, false
}

func TestAToleratedHeaderNobodyConfiguredIsAttributedToTheBuiltin(t *testing.T) {
	s := summaryWithEntries(Entry{
		HeaderDiff: []HeaderDiff{{Scope: "resp", Name: "date", Type: "tolerated", Matcher: "http-date"}},
	})
	got := suppressionsOf(s, &config.Config{})
	row, ok := findSuppression(got, "header", "date")
	if !ok {
		t.Fatalf("no row for the date header: %+v", got)
	}
	if row.Source != SourceBuiltin {
		t.Errorf("Source = %q, want %q — nobody wrote this rule", row.Source, SourceBuiltin)
	}
	if row.Matcher != "http-date" || row.Count != 1 {
		t.Errorf("row = %+v, want matcher http-date ×1", row)
	}
}

func TestOverridingABuiltinReattributesTheRowToTheUser(t *testing.T) {
	// The point of telling the sources apart: after the user writes their
	// own `date` rule, the report must send them to their config rather
	// than to retrace's built-ins.
	cfg := &config.Config{WireRules: []rules.Raw{{Headers: map[string]any{"Date": "iso8601"}}}}
	s := summaryWithEntries(Entry{
		HeaderDiff: []HeaderDiff{{Scope: "resp", Name: "date", Type: "tolerated", Matcher: "iso8601"}},
	})
	row, ok := findSuppression(suppressionsOf(s, cfg), "header", "date")
	if !ok {
		t.Fatal("no row for the date header")
	}
	// The config spells it "Date" and the diff spells it "date": header
	// names are compared case-insensitively everywhere else, and a source
	// lookup that forgot would silently report this as a built-in.
	if row.Source != SourceWireRule {
		t.Errorf("Source = %q, want %q", row.Source, SourceWireRule)
	}
}

func TestAnIgnoredHeaderIsCountedAtAll(t *testing.T) {
	// Before HeaderIgnored existed this was the one suppression in the
	// engine that left no trace anywhere: DiffHeaders dropped it with a
	// bare `continue`.
	res := rules.Resolved{Headers: map[string]rules.Matcher{"x-trace-id": mustMatcher(t, "ignore")}}
	diffs, ignored := DiffHeaders(
		map[string]string{"x-trace-id": "abc"},
		map[string]string{"x-trace-id": "xyz"},
		res, "resp",
	)
	if len(diffs) != 0 {
		t.Fatalf("an ignored header must stay out of the findings list, got %+v", diffs)
	}
	if len(ignored) != 1 || ignored[0].Name != "x-trace-id" || ignored[0].Type != "ignored" {
		t.Fatalf("ignored = %+v, want one x-trace-id entry typed ignored", ignored)
	}
	if ignored[0].Matcher != "ignore" {
		t.Errorf("Matcher = %q, want %q", ignored[0].Matcher, "ignore")
	}

	cfg := &config.Config{WireRules: []rules.Raw{{Headers: map[string]any{"x-trace-id": "ignore"}}}}
	row, ok := findSuppression(suppressionsOf(summaryWithEntries(Entry{HeaderIgnored: ignored}), cfg), "header", "x-trace-id")
	if !ok {
		t.Fatal("an ignored header must reach the suppression report")
	}
	if row.Source != SourceWireRule || row.Count != 1 {
		t.Errorf("row = %+v, want a wire_rule ×1", row)
	}
}

func TestAnIgnoredHeaderStillDoesNotMakeTheCallChangedOrMoveTriage(t *testing.T) {
	// The reason HeaderIgnored is its own array rather than a new Type in
	// HeaderDiff. classify() counts every non-"tolerated" HeaderDiff as a
	// real change and triage reads the same list, so folding ignored
	// entries in would have turned each one into both a "changed" call and
	// a triage signal — the exact opposite of what `ignore` means.
	e := Entry{
		Method: "GET", NormalizedPath: "/x",
		HeaderIgnored: []HeaderDiff{{Scope: "resp", Name: "x-trace-id", Type: "ignored", Matcher: "ignore"}},
	}
	if got := classify(e); len(got) != 1 || got[0] != "identical" {
		t.Errorf("classify = %v, want [identical] — an ignored header is not a change", got)
	}
	s := summaryWithEntries(e)
	s.Counts = countOf(s)
	if sig := signalsOf(s); sig.Wire || sig.Hop {
		t.Errorf("signals = %+v, want no wire/hop signal from an ignored header", sig)
	}
}

func TestABodyGlobIsAttributedToWireIgnoreOrWireRuleByWhereItCameFrom(t *testing.T) {
	cfg := &config.Config{
		WireRules:  []rules.Raw{{Body: map[string]any{"**.version": "semver"}}},
		WireIgnore: []config.WireIgnoreEntry{{Path: "**.requestId", Why: "regenerated"}},
	}
	s := summaryWithEntries(Entry{
		BodyTolerated: []FieldDiff{{Scope: "resp", Path: "a.version", Glob: "**.version", Matcher: "semver"}},
		BodyIgnored:   []FieldDiff{{Scope: "resp", Path: "a.requestId", Glob: "**.requestId", Matcher: "ignore"}},
	})
	got := suppressionsOf(s, cfg)

	rule, ok := findSuppression(got, "body", "**.version")
	if !ok || rule.Source != SourceWireRule {
		t.Errorf("**.version row = %+v (found=%v), want source %q", rule, ok, SourceWireRule)
	}
	ign, ok := findSuppression(got, "body", "**.requestId")
	if !ok || ign.Source != SourceWireIgnore {
		t.Errorf("**.requestId row = %+v (found=%v), want source %q", ign, ok, SourceWireIgnore)
	}
}

func TestAGlobInBothARuleAndTheIgnoreListIsCreditedToTheRule(t *testing.T) {
	// resolveField consults rules before the ignore list, so the rule is
	// what actually fired. A report that credited the ignore list would
	// send someone to delete an entry that is doing nothing.
	cfg := &config.Config{
		WireRules:  []rules.Raw{{Body: map[string]any{"**.id": "uuid"}}},
		WireIgnore: []config.WireIgnoreEntry{{Path: "**.id"}},
	}
	s := summaryWithEntries(Entry{
		BodyTolerated: []FieldDiff{{Scope: "resp", Path: "a.id", Glob: "**.id", Matcher: "uuid"}},
	})
	row, _ := findSuppression(suppressionsOf(s, cfg), "body", "**.id")
	if row.Source != SourceWireRule {
		t.Errorf("Source = %q, want %q", row.Source, SourceWireRule)
	}
}

func TestCountsAggregateAcrossCallsAndLoudestSortsFirst(t *testing.T) {
	date := HeaderDiff{Scope: "resp", Name: "date", Type: "tolerated", Matcher: "http-date"}
	etag := HeaderDiff{Scope: "resp", Name: "etag", Type: "tolerated", Matcher: "etag"}
	s := summaryWithEntries(
		Entry{HeaderDiff: []HeaderDiff{date, etag}},
		Entry{HeaderDiff: []HeaderDiff{date}},
		Entry{HeaderDiff: []HeaderDiff{date}},
	)
	got := suppressionsOf(s, &config.Config{})
	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2: %+v", len(got), got)
	}
	if got[0].Target != "date" || got[0].Count != 3 {
		t.Errorf("first row = %+v, want date ×3 — loudest first", got[0])
	}
	if got[1].Target != "etag" || got[1].Count != 1 {
		t.Errorf("second row = %+v, want etag ×1", got[1])
	}
}

func TestOnlyToleratedHeadersCountAsSuppressions(t *testing.T) {
	// A changed/violating/added/removed header is a FINDING, not a
	// suppression. Counting those would make the report claim rules hid
	// differences that are sitting in the diff in plain sight.
	s := summaryWithEntries(Entry{HeaderDiff: []HeaderDiff{
		{Scope: "resp", Name: "x-a", Type: "changed"},
		{Scope: "resp", Name: "x-b", Type: "violation", Matcher: "etag"},
		{Scope: "resp", Name: "x-c", Type: "added"},
		{Scope: "resp", Name: "x-d", Type: "removed"},
	}})
	if got := suppressionsOf(s, &config.Config{}); len(got) != 0 {
		t.Errorf("got %+v, want no rows", got)
	}
}

func TestARuleThatSuppressedNothingProducesNoRow(t *testing.T) {
	// The report answers "what did your rules hide", not "what rules do you
	// have". A config full of tolerances over a run where every value
	// matched must report nothing.
	cfg := &config.Config{
		WireRules:  []rules.Raw{{Body: map[string]any{"**.version": "semver"}}},
		WireIgnore: []config.WireIgnoreEntry{{Path: "**.requestId"}},
	}
	got := suppressionsOf(summaryWithEntries(Entry{Method: "GET", NormalizedPath: "/x"}), cfg)
	if len(got) != 0 {
		t.Errorf("got %+v, want no rows", got)
	}
}

func TestSuppressionsIsNeverNil(t *testing.T) {
	// Summary.suppressions is a documented array. `null` reads to a JSON
	// consumer as "this engine does not report suppressions", which is a
	// different claim from "none fired".
	if got := suppressionsOf(Summary{}, nil); got == nil {
		t.Error("suppressionsOf returned nil, want an empty slice")
	}
	var s Summary
	s.finish(nil)
	if s.Suppressions == nil {
		t.Error("finish left Suppressions nil")
	}
}

func TestANilConfigStillAttributesTheBuiltins(t *testing.T) {
	// `retrace run --no-config` and a directory with no retrace.yaml both
	// carry the built-ins. Reporting those as "wire_rule" would point at a
	// file that does not exist.
	s := summaryWithEntries(Entry{
		HeaderDiff: []HeaderDiff{{Scope: "resp", Name: "date", Type: "tolerated", Matcher: "http-date"}},
	})
	row, ok := findSuppression(suppressionsOf(s, nil), "header", "date")
	if !ok || row.Source != SourceBuiltin {
		t.Errorf("row = %+v (found=%v), want source %q", row, ok, SourceBuiltin)
	}
}

func TestRenderTextNamesTheSuppressionsAndTheirTotal(t *testing.T) {
	var s Summary
	s.Wire.Paired = []Entry{{HeaderDiff: []HeaderDiff{
		{Scope: "resp", Name: "date", Type: "tolerated", Matcher: "http-date"},
	}}}
	s.Verdict = "pass"
	s.finish(&config.Config{})
	var b strings.Builder
	RenderText(&b, s)
	out := b.String()
	if !strings.Contains(out, "SUPPRESSED: 1 difference(s) across 1 rule(s)") {
		t.Errorf("render must total the suppressions, got:\n%s", out)
	}
	for _, want := range []string{"date", "builtin", "http-date"} {
		if !strings.Contains(out, want) {
			t.Errorf("render must name %q, got:\n%s", want, out)
		}
	}
	// Before the verdict, or a CI reader scrolling to the last line never
	// learns the clean verdict was bought by a rule.
	if strings.Index(out, "SUPPRESSED:") > strings.Index(out, "VERDICT:") {
		t.Error("SUPPRESSED must print before VERDICT")
	}
}

func TestRenderTextSaysNothingWhenNoRuleFired(t *testing.T) {
	var s Summary
	s.Verdict = "pass"
	s.finish(&config.Config{})
	var b strings.Builder
	RenderText(&b, s)
	if strings.Contains(b.String(), "SUPPRESSED") {
		t.Errorf("an empty heading trains the reader to skip it, got:\n%s", b.String())
	}
}
