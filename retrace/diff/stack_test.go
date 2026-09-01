package diff

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/retrace/config"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

// sideWithStack builds one comparable side carrying a stack record. Capture
// is ok on both sides deliberately: a quarantined comparison never reaches
// the triage table, so a fixture that forgot the verdict would test the
// harness path instead of the one under test.
func sideWithStack(st *runs.Stack) RunRef {
	return RunRef{
		Kind: "run",
		Manifest: runs.Manifest{
			Stack:   st,
			Capture: runs.CaptureTrust{Status: trace.VerdictOK},
		},
	}
}

func services(pairs ...string) *runs.Stack {
	s := &runs.Stack{Services: map[string]string{}}
	for i := 0; i+1 < len(pairs); i += 2 {
		s.Services[pairs[i]] = pairs[i+1]
	}
	return s
}

// firstBuiltIn is the built-in table's own resolution, run over a vector the
// test supplies directly. Two of the tests below are about the table's
// ORDERING rather than about producing real plane differences, and building a
// Summary that genuinely moves two planes at once would test the plane
// builders instead of the precedence claim.
func firstBuiltIn(t *testing.T, sig TriageSignals) config.TriageRule {
	t.Helper()
	for _, r := range defaultTriageRules {
		if matches(r, sig) {
			return r
		}
	}
	t.Fatalf("the built-in table matched nothing for %+v — it is supposed to be total", sig)
	return config.TriageRule{}
}

func buildStackDiff(t *testing.T, a, b *runs.Stack) Summary {
	t.Helper()
	s, err := Build(BuildInput{
		App: "shop", Flow: "checkout",
		A: sideWithStack(a), B: sideWithStack(b),
		Cfg: &config.Config{},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return s
}

func TestARedeployedServiceIsReportedAsAStackChange(t *testing.T) {
	s := buildStackDiff(t, services("api", "abc", "web", "111"), services("api", "abc", "web", "222"))
	if s.Stack == nil {
		t.Fatal("Summary.Stack is nil for two runs whose fingerprints disagree")
	}
	if len(s.Stack.Changed) != 1 || s.Stack.Changed[0] != "web" {
		t.Errorf("changed = %v, want [web]", s.Stack.Changed)
	}
}

func TestAnUnchangedStackReportsNothingAtAll(t *testing.T) {
	// Nil, not an empty StackDiff. `stack: {}` in the JSON would read as
	// "something about the stack was examined and found to differ", and a
	// consumer checking for the key's presence would see it on every run.
	if s := buildStackDiff(t, services("api", "abc"), services("api", "abc")); s.Stack != nil {
		t.Errorf("Summary.Stack = %+v for two identical stacks, want nil", s.Stack)
	}
	if s := buildStackDiff(t, nil, nil); s.Stack != nil {
		t.Errorf("Summary.Stack = %+v when neither side recorded one, want nil", s.Stack)
	}
}

func TestReducedScopeNamesAPassthroughServiceOnEitherSide(t *testing.T) {
	withPassthrough := &runs.Stack{Passthrough: []string{"edge"}}
	s, err := Build(BuildInput{
		App: "shop", Flow: "checkout",
		A: sideWithStack(withPassthrough), B: sideWithStack(nil),
		Cfg: &config.Config{},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(s.ReducedScope) != 1 || s.ReducedScope[0] != "edge" {
		t.Errorf("reducedScope = %v, want [edge] even though only side A recorded it", s.ReducedScope)
	}
}

func TestReducedScopeMergesAndDedupesBothSides(t *testing.T) {
	s, err := Build(BuildInput{
		App: "shop", Flow: "checkout",
		A:   sideWithStack(&runs.Stack{Passthrough: []string{"edge", "billing"}}),
		B:   sideWithStack(&runs.Stack{Passthrough: []string{"billing", "search"}}),
		Cfg: &config.Config{},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(s.ReducedScope) != 3 {
		t.Fatalf("reducedScope = %v, want 3 unique names", s.ReducedScope)
	}
	want := map[string]bool{"edge": true, "billing": true, "search": true}
	for _, name := range s.ReducedScope {
		if !want[name] {
			t.Errorf("unexpected name %q in reducedScope", name)
		}
	}
}

func TestReducedScopeIsNilWhenNeitherSideHasAPassthroughService(t *testing.T) {
	if s := buildStackDiff(t, services("api", "abc"), services("api", "abc")); s.ReducedScope != nil {
		t.Errorf("reducedScope = %v, want nil", s.ReducedScope)
	}
}

func TestAChangedStackOutranksTheClientOnTriage(t *testing.T) {
	// The whole reason this section exists. With the client's calls identical
	// and only the backend moved, the report must not send anyone to the
	// client repository.
	s := buildStackDiff(t, services("api", "old"), services("api", "new"))
	if s.Triage.Label != TriageStack {
		t.Errorf("triage label = %q, want %q", s.Triage.Label, TriageStack)
	}
	if s.Triage.Rule != "stack-changed" {
		t.Errorf("triage rule = %q, want stack-changed", s.Triage.Rule)
	}
	if !s.Triage.Signals.Stack {
		t.Error("the stack signal is false on a run whose stack changed")
	}
}

func TestAChangedStackOutranksAChangedWire(t *testing.T) {
	// The precedence claim, stated where it is checkable. A backend that
	// moved between the two runs can CAUSE the client to call differently —
	// a different response is what the next call is computed from — so
	// "client-behavior" against a redeployed stack is the misattribution the
	// signal exists to prevent.
	a, b := sideWithStack(services("api", "old")), sideWithStack(services("api", "new"))
	s, err := Build(BuildInput{
		App: "shop", Flow: "checkout", A: a, B: b, Cfg: &config.Config{},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	// Force the wire signal on by hand, then re-classify: this test is about
	// the table's ordering, not about producing a wire difference.
	sig := signalsOf(s)
	sig.Wire = true
	rule := firstBuiltIn(t, sig)
	if rule.Label != TriageStack {
		t.Errorf("with stack AND wire moved the label is %q, want %q — the client is downstream of the backend it called", rule.Label, TriageStack)
	}
}

func TestABrokenRecordingStillOutranksAChangedStack(t *testing.T) {
	// Capture stays at the top. A recording that cannot be trusted makes
	// every statement below it, including this one, confident nonsense.
	rule := firstBuiltIn(t, TriageSignals{Capture: true, Stack: true})
	if rule.Label != TriageHarness {
		t.Errorf("label = %q, want %q", rule.Label, TriageHarness)
	}
}

func TestADifferentSeedIsReportedWithBothNames(t *testing.T) {
	at := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	a := &runs.Stack{Seed: &runs.SeedRef{Name: "baseline", AppliedAt: at}}
	b := &runs.Stack{Seed: &runs.SeedRef{Name: "promo-week", AppliedAt: at}}
	s := buildStackDiff(t, a, b)
	if s.Stack == nil {
		t.Fatal("Summary.Stack is nil for two runs seeded differently")
	}
	for _, want := range []string{"baseline", "promo-week"} {
		if !strings.Contains(s.Stack.SeedA+" "+s.Stack.SeedB, want) {
			t.Errorf("the seed report omits %q: A=%q B=%q", want, s.Stack.SeedA, s.Stack.SeedB)
		}
	}
	// Data the runs started from is not traffic, so it must not be reported
	// as a service that changed.
	if len(s.Stack.Changed) != 0 {
		t.Errorf("changed = %v, want empty — no service fingerprint moved", s.Stack.Changed)
	}
	// And it must reach the RENDERED report, not only --json. A seed
	// difference recorded where nobody reading a CI log will see it explains
	// nothing to the person the explanation is for.
	var buf bytes.Buffer
	RenderText(&buf, s)
	out := buf.String()
	if !strings.Contains(out, "baseline") || !strings.Contains(out, "promo-week") {
		t.Errorf("the report does not name both seeds:\n%s", out)
	}
}

func TestTheBuiltInTableNamesAtMostOneCauseForAnyRun(t *testing.T) {
	// The mutual-exclusivity invariant the table's own comment claims, over
	// every one of the 64 possible vectors. Ordering alone would hide a
	// missing "everything above is same" constraint — the first matching row
	// still wins — until someone reorders the rows or a project rule lands in
	// front of them, at which point two rows disagree about the same run.
	for bits := 0; bits < 64; bits++ {
		sig := TriageSignals{
			Capture: bits&1 != 0,
			Stack:   bits&2 != 0,
			Wire:    bits&4 != 0,
			Hop:     bits&8 != 0,
			Spec:    bits&16 != 0,
			Pixel:   bits&32 != 0,
		}
		var matched []string
		for _, r := range defaultTriageRules {
			if matches(r, sig) {
				matched = append(matched, r.Name)
			}
		}
		if len(matched) > 1 {
			t.Errorf("%+v matches %v — two rows claim the same run", sig, matched)
		}
		// Totality: every vector with a signal in it must land somewhere. The
		// no-signal vector is the one exception, handled below the table by
		// triageOf's pass/unclassified split.
		if bits != 0 && len(matched) == 0 {
			t.Errorf("%+v matches nothing — the table is supposed to be total", sig)
		}
	}
}

func TestTheTextReportNamesTheServiceBeforeItNamesACulprit(t *testing.T) {
	// Ordering in the rendered report, not just in the JSON. Someone reading
	// a CI log must see that the backend moved before they read the triage
	// line that would otherwise send them to their own diff.
	s := buildStackDiff(t, services("api", "old"), services("api", "new"))
	var buf bytes.Buffer
	RenderText(&buf, s)
	out := buf.String()

	stackAt := strings.Index(out, "STACK:")
	triageAt := strings.Index(out, "TRIAGE:")
	if stackAt < 0 {
		t.Fatalf("the report has no STACK line:\n%s", out)
	}
	if !strings.Contains(out, "api") {
		t.Errorf("the STACK line does not name the service:\n%s", out)
	}
	if triageAt >= 0 && stackAt > triageAt {
		t.Errorf("STACK is printed after TRIAGE; the explanation must precede the accusation:\n%s", out)
	}
}

func TestAnUnchangedStackPrintsNoStackLine(t *testing.T) {
	// A heading on every clean run trains the reader to skip the section on
	// the runs where it matters.
	s := buildStackDiff(t, services("api", "abc"), services("api", "abc"))
	var buf bytes.Buffer
	RenderText(&buf, s)
	if strings.Contains(buf.String(), "STACK:") {
		t.Errorf("a STACK line was printed for two identical stacks:\n%s", buf.String())
	}
}
