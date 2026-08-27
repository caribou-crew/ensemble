// triage.go answers the question every other field on the Summary leaves to
// the reader: given that something moved, WHOSE problem is it. The four
// planes each report what changed; none of them reports what to do about it,
// and an agent reading them in isolation will confidently start editing a
// client that never changed.
//
// The classification is a first-match table over five moved/same signals.
// Deliberately not a scoring function or a heuristic: a reader must be able
// to look at `triage.signals`, look at the rule named in `triage.rule`, and
// reproduce the label by hand. A label nobody can check is a label nobody
// should act on.
package diff

import (
	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/retrace/config"
)

// The built-in labels. Named constants because they appear in the report
// copy, in --json, in the retrace-iterate recipe, and in the docs contract
// test that pins those to each other.
const (
	// TriageHarness — the recording itself is not trustworthy. Nothing below
	// this line means anything until it is fixed.
	TriageHarness = "harness"
	// TriageClientBehavior — the client sent something different: a call it
	// did not make before, did not make this time, made in a different order,
	// or made with a different request body or headers.
	TriageClientBehavior = "client-behavior"
	// TriageStack — the client sent the identical requests and the stack
	// answered differently: a different status, response body or response
	// headers at the client edge, or a moved cross-service chain.
	TriageStack = "stack"
	// TriageContractDrift — identical traffic, and it no longer matches the
	// OpenAPI spec. With wire and hop both unmoved the traffic did not
	// change, so the spec did.
	TriageContractDrift = "contract-drift"
	// TriageClientUI — a rendering change, with no observable traffic change.
	TriageClientUI = "client-ui"
	// TriageNone — no signal moved and the verdict is clean. There is nothing
	// to attribute.
	TriageNone = "none"
	// TriageUnclassified — no signal moved, and the run is still not a pass.
	// The five signals do not cover perf budgets, unexpected statuses,
	// hopRequire failures or unevaluated gates, and inventing a plane for
	// them would be inventing a cause: those failures are reported in
	// `gates`, which is where this label points the reader. NOT a synonym for
	// "clean" — see triageOf.
	TriageUnclassified = "unclassified"
)

// TriageSignals is the moved/same vector the table matches on: true means
// that plane moved.
//
// Carried on the wire alongside the label because the label alone is not
// checkable. Three of the five are not derivable from the rest of the
// Summary without re-implementing this file — `hop` folds NewRoutes,
// GoneRoutes and per-service count deviation into one bit, `spec` excludes
// "unchecked" findings, and `capture` is true for a quarantine whose capture
// verdict is itself "ok" (a signal-killed run). A consumer that re-derived
// them would drift, and Summary.UnmeasuredGates exists because exactly that
// happened once already.
type TriageSignals struct {
	Pixel   bool `json:"pixel"`
	Wire    bool `json:"wire"`
	Hop     bool `json:"hop"`
	Spec    bool `json:"spec"`
	Capture bool `json:"capture"`
	// Stack is the one signal that is not a plane of the comparison. The
	// other five describe what differs BETWEEN the two runs; this one
	// describes what differed about the conditions they were recorded under,
	// which is why it can explain any of the others.
	Stack bool `json:"stack"`
}

// Triage is the classification: what kind of problem this is, and which rule
// said so.
type Triage struct {
	// Label is one of the Triage* constants, or any string a project's own
	// `triage:` rule chose. Never empty on a Summary that Build returned —
	// the built-in table is total over the 32 possible signal vectors, and an
	// empty label would be a sixth meaning nobody can read.
	Label string `json:"label"`
	// Rule names the row that matched: a built-in name ("wire-moved"), or the
	// project rule's own `name:` / its `triage[N]` index. Without it the
	// label is an assertion the reader cannot trace back to anything.
	Rule    string        `json:"rule"`
	Signals TriageSignals `json:"signals"`
}

// defaultTriageRules is the built-in table, in priority order. Every rule
// names ALL the signals above it as "same", so the rows are mutually
// exclusive and the table is a total function over the five bits: read it as
// "the first signal that moved, in the order capture → wire → hop → spec →
// pixel, names the label".
//
// That order is a claim about causation, not about severity:
//
//   - capture first because a recording that is not trustworthy makes every
//     plane below it confident nonsense.
//   - stack second, above every traffic plane, because a backend that changed
//     between the two runs can cause ANY of them to move — including wire,
//     since a different response is what the client's next call is computed
//     from. Reporting client-behavior against a redeployed stack is the exact
//     misattribution this signal exists to prevent, and it is the one the
//     reader is least equipped to catch: the client is the only thing their
//     test touched, so it is the only thing they think to inspect.
//   - wire above hop because a client making different calls is the CAUSE of
//     the chain differing; reporting "stack" there sends the reader to the
//     wrong repository.
//   - hop above spec and pixel for the same reason one level down: if the
//     stack answered differently, a spec finding or a repainted screen is
//     downstream of that.
//   - spec above pixel because with wire and hop both unmoved the traffic is
//     identical, so a conformance finding means the SPEC moved — a contract
//     change, which is the more consequential of the two and the one more
//     easily missed. A repaint on top of it is coincidental.
//
// Expressed as config.TriageRule values, not as a Go switch, so the built-in
// rows and a project's own rows go through ONE matcher. A second code path
// for the defaults is a second set of precedence bugs.
var defaultTriageRules = []config.TriageRule{{
	Name:  "capture-not-ok",
	Label: TriageHarness,
	When:  config.TriageWhen{Capture: config.TriageMoved},
}, {
	Name:  "stack-changed",
	Label: TriageStack,
	When:  config.TriageWhen{Capture: config.TriageSame, Stack: config.TriageMoved},
}, {
	Name:  "wire-moved",
	Label: TriageClientBehavior,
	When:  config.TriageWhen{Capture: config.TriageSame, Stack: config.TriageSame, Wire: config.TriageMoved},
}, {
	Name:  "hop-only",
	Label: TriageStack,
	When: config.TriageWhen{
		Capture: config.TriageSame, Stack: config.TriageSame,
		Wire: config.TriageSame, Hop: config.TriageMoved,
	},
}, {
	Name:  "spec-only",
	Label: TriageContractDrift,
	When: config.TriageWhen{
		Capture: config.TriageSame, Stack: config.TriageSame, Wire: config.TriageSame,
		Hop: config.TriageSame, Spec: config.TriageMoved,
	},
}, {
	Name:  "pixel-only",
	Label: TriageClientUI,
	When: config.TriageWhen{
		Capture: config.TriageSame, Stack: config.TriageSame, Wire: config.TriageSame,
		Hop: config.TriageSame, Spec: config.TriageSame, Pixel: config.TriageMoved,
	},
}}

// TriageLabels returns every label the BUILT-IN table can produce, in
// priority order, followed by the two no-signal labels.
//
// Derived from the table itself rather than being a second hand-kept list:
// this is what the agent recipe's drift guard checks the documentation
// against, and a hand-kept list would become the thing that drifts — the
// failure mode Summary.UnmeasuredGates was created to fix one field over.
//
// It does NOT include labels a project defines under `triage:`. Those are
// ordinary configuration, not values of a closed enum, which is why no
// consumer may switch exhaustively on a label.
func TriageLabels() []string {
	out := make([]string, 0, len(defaultTriageRules)+2)
	for _, r := range defaultTriageRules {
		out = append(out, r.Label)
	}
	return append(out, TriageNone, TriageUnclassified)
}

// signalOf reads one named signal off the vector. The name vocabulary is
// shared with config.TriageWhen.Constraints, and an unknown name reports
// false for BOTH returns rather than defaulting to "unconstrained": a rule
// naming a signal this build does not know must fail to match, never match
// everything. Load rejects such a name outright, so this is the belt to that
// braces — but it is the belt that decides what happens if the two ever
// disagree.
func signalOf(s TriageSignals, name string) (moved bool, known bool) {
	switch name {
	case "pixel":
		return s.Pixel, true
	case "wire":
		return s.Wire, true
	case "hop":
		return s.Hop, true
	case "spec":
		return s.Spec, true
	case "capture":
		return s.Capture, true
	case "stack":
		return s.Stack, true
	}
	return false, false
}

// matches reports whether every constraint the rule names holds for s. A rule
// that names no constraint does NOT match — validateTriage rejects such a
// rule at Load, and matching everything would be the worse of the two
// failures if one ever reached here.
func matches(r config.TriageRule, s TriageSignals) bool {
	cs := r.When.Constraints()
	if len(cs) == 0 {
		return false
	}
	for _, c := range cs {
		moved, known := signalOf(s, c[0])
		if !known {
			return false
		}
		want := c[1] == config.TriageMoved
		if moved != want {
			return false
		}
	}
	return true
}

// signalsOf reduces the whole Summary to the five bits the table reads.
//
// The five are CAUSES, not planes. That distinction is load-bearing for the
// two wire-derived bits and was found by reading real `retrace diff` output:
// against a stack whose response body changed and a client that made the
// identical request, "the wire plane moved" is true and "the client is making
// different calls" is false. Attributing every wire movement to the client
// misdirects the single most common real change there is.
//
// So the wire plane is split by SCOPE — "req" | "resp" on every FieldDiff and
// HeaderDiff — and each half feeds the bit whose label it actually supports:
//
//   - wire — the client behaved differently: a call missing, extra or
//     reordered, or a request whose headers or body changed. Things the
//     client decided.
//   - hop — the stack answered differently: the cross-service chain moved
//     (new/gone routes, deviating per-service counts), OR a response at the
//     client edge changed — its status, headers or body. Things the client
//     only received.
//
// The response half matters most on a STANDALONE run, which records no
// hops.jsonl at all: without it, a changed response has no bit to move, and
// the only reading left is the client's fault.
//
// The other three mirror existing rules rather than inventing new ones:
//
//   - pixel: any checkpoint whose verdict is not "ok" — the same test
//     countOf uses for Counts.PixelChanged, so "5 pixel changes and the
//     pixel signal is false" cannot happen.
//   - spec: a conformance finding whose Kind is not "unchecked", matching
//     changed(). An "unchecked" finding is the absence of evidence, not
//     drift, and is reported on its own labelled line by renderConformance.
//   - capture: a non-ok trust verdict on EITHER side, or any quarantine at
//     all. The quarantine clause is not redundant: incompleteCheck
//     quarantines a signal-killed run whose capture verdict is a perfectly
//     ok "ok", and that run is a harness problem by any reading.
//
// Tolerated and ignored differences move nothing, on either half — the same
// rule Counts already applies to calls an approved deviation covers. A
// difference someone has excused in writing is not evidence of a cause.
func signalsOf(s Summary) TriageSignals {
	var sig TriageSignals
	for _, cp := range s.Checkpoints {
		if cp.Verdict != "ok" {
			sig.Pixel = true
			break
		}
	}
	c := s.Counts
	// A call the candidate did not make, made extra, or made in a different
	// order is the client's decision in every case — there is no response to
	// attribute it to.
	sig.Wire = c.WireMissing > 0 || c.WireExtra > 0 || c.WireMoved > 0
	sig.Hop = c.HopNew > 0 || c.HopGone > 0
	for _, svc := range s.Hops.ServiceCounts {
		if svc.Deviates {
			sig.Hop = true
			break
		}
	}

	// side routes one scoped difference to its cause. Anything that is not
	// explicitly a request is treated as a response, so a scope this build
	// does not recognise lands on "the stack answered differently" — the
	// direction that sends a reader to look rather than to edit.
	attributed := false
	side := func(scope string) {
		attributed = true
		if scope == "req" {
			sig.Wire = true
			return
		}
		sig.Hop = true
	}
	for _, e := range s.Wire.Paired {
		// A status change is a response, always: the client asks, the stack
		// answers with a code.
		if e.StatusChange != nil {
			side("resp")
		}
		if e.Moved {
			side("req")
		}
		// BodyTolerated and BodyIgnored are deliberately absent from this
		// list. BodyDiff is already the untolerated set.
		for _, group := range [][]FieldDiff{e.BodyDiff, e.BodyViolations, e.OrderingChanges} {
			for _, fd := range group {
				side(fd.Scope)
			}
		}
		for _, hd := range e.HeaderDiff {
			// "tolerated" is a header change a rule excused in writing, and
			// classify already keeps it out of the entry's "changed" class.
			if hd.Type == "tolerated" {
				continue
			}
			side(hd.Scope)
		}
	}
	// The backstop. If the wire plane counted a changed entry and nothing
	// above could say which side of it moved, the run must not fall through
	// to "no signal moved" — a changed run reporting `unclassified` is a
	// worse answer than an imprecise one. Attributed to the client because
	// that is the reader's own diff: it sends them to look at what they
	// changed, not to open somebody else's repository.
	if !attributed && c.WireChanged > 0 {
		sig.Wire = true
	}

	for _, f := range s.Conformance {
		if f.Kind != "unchecked" {
			sig.Spec = true
			break
		}
	}
	sig.Capture = len(s.Quarantined) > 0 ||
		s.Capture.A.Status != trace.VerdictOK ||
		s.Capture.B.Status != trace.VerdictOK
	// Only a DEMONSTRATED difference: Stack is nil when neither side recorded
	// a fingerprint, or when the two share none. Absence is not evidence, the
	// same rule geometryCheck follows — otherwise every run recorded before
	// this field existed, and every standalone run, would report its backend
	// as having changed.
	sig.Stack = s.Stack != nil
	return sig
}

// triageOf classifies a Summary. Call it LAST in Build: it reads Counts and
// Verdict, both of which are derived from every plane.
//
// A QUARANTINED Summary never reaches the table. Build returns from a
// quarantine before a single plane is computed, so the four traffic signals
// are all false for want of data rather than for want of differences — and
// running the table over that vector would let a project rule matching
// "wire: same, pixel: same" relabel a refused comparison as a clean one.
// Nothing was compared; there is nothing to attribute; the answer is
// harness, and it is not overridable.
func triageOf(s Summary, cfg *config.Config) Triage {
	sig := signalsOf(s)
	if s.Verdict == "quarantined" {
		return Triage{Label: TriageHarness, Rule: "quarantined", Signals: sig}
	}
	var rules []config.TriageRule
	if cfg != nil {
		rules = append(rules, cfg.Triage...)
	}
	rules = append(rules, defaultTriageRules...)
	for _, r := range rules {
		if matches(r, sig) {
			return Triage{Label: r.Label, Rule: r.Name, Signals: sig}
		}
	}
	// Nothing moved. The two ways to arrive here are not the same fact and
	// must not share a label: a genuine pass, and a run that failed on
	// something the five signals do not cover (a perf budget, an unexpected
	// status, a hopRequire route, an unevaluated gate). Reporting the second
	// as "none" would be this codebase's own zero-value trap wearing a new
	// hat — a reassuring value on the run that most needs reading.
	if s.Verdict == "pass" {
		return Triage{Label: TriageNone, Rule: "no-signal-moved", Signals: sig}
	}
	return Triage{Label: TriageUnclassified, Rule: "no-signal-moved", Signals: sig}
}
