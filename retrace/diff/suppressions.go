package diff

import (
	"sort"
	"strings"

	"github.com/caribou-crew/ensemble/retrace/config"
)

// The three places a tolerance can come from, as they appear in
// Suppression.Source.
const (
	// SourceWireRule is a `wire_rules:` entry — hand-written in
	// retrace.yaml, or appended to .retrace/wire-rules.json by the review
	// queue's `rule` verb. The two are indistinguishable here on purpose:
	// Discover merges the overlay into WireRules before anything reads it,
	// and both are equally the project's own rule.
	SourceWireRule = "wire_rule"
	// SourceWireIgnore is a `wire_ignore:` body field-path glob.
	SourceWireIgnore = "wire_ignore"
	// SourceBuiltin is one of the header tolerances retrace ships with
	// (config.BuiltinWireRules) and nobody in this project asked for.
	// Worth telling apart from the other two: a project inherits these
	// without writing a line, so a `date` row firing 29 times is retrace
	// explaining itself, whereas a wire_ignore firing 29 times is a
	// decision someone here made and can revisit.
	SourceBuiltin = "builtin"
)

// Suppression is one tolerance that actually silenced a difference in this
// run, and how many times it fired.
//
// "Actually silenced" is the whole point, and it is inherited rather than
// recomputed: the engine only records a FieldDiff in BodyTolerated /
// BodyIgnored, or a HeaderDiff in HeaderIgnored / with Type "tolerated",
// when the two sides genuinely differed and a matcher excused it. A rule
// covering a field whose values already matched is not listed here, because
// it suppressed nothing.
type Suppression struct {
	Plane   string `json:"plane"`   // "header" | "body"
	Target  string `json:"target"`  // header name, or the body field-path glob that matched
	Source  string `json:"source"`  // "wire_rule" | "wire_ignore" | "builtin"
	Matcher string `json:"matcher"` // "ignore", "http-date", "/v\\d+/", …
	Count   int    `json:"count"`
	// Why is the `why:` the config gave this tolerance, verbatim, or "" when
	// it gave none. It is what turns the row from a fact into something a
	// reader can judge: "date builtin http-date ×29" says a rule fired,
	// while the reason beside it says whether the excuse still holds.
	//
	// omitempty, and NOT defaulted to a placeholder: an absent why must read
	// as absent. Inventing "no reason given" text here would put words in
	// the config's mouth and make an un-explained tolerance look documented
	// in every consumer that prints the field.
	Why string `json:"why,omitempty"`
}

// suppressionSource answers where a fired tolerance came from. It is built
// once per summary from the config rather than threaded through the diff,
// because provenance is a property of the CONFIG, not of the comparison —
// and pushing it down into FieldDiff/HeaderDiff would widen two wire types
// every consumer reads to carry something only this report wants.
type suppressionSource struct {
	userHeaders map[string]string // lower-cased header name → its rule's why
	userGlobs   map[string]string // body glob → its rule's why
	ignoreGlobs map[string]string // wire_ignore glob → its why
	builtins    map[string]bool
}

func newSuppressionSource(cfg *config.Config) suppressionSource {
	s := suppressionSource{
		userHeaders: map[string]string{},
		userGlobs:   map[string]string{},
		ignoreGlobs: map[string]string{},
		builtins:    map[string]bool{},
	}
	// Built-ins are populated even for a nil cfg: config.Discover with no
	// retrace.yaml on disk hands back a defaulted Config that still carries
	// them, and `retrace run --no-config` proceeds on one. A report that
	// called those rows "wire_rule" would be pointing at a file that does
	// not exist.
	for _, name := range config.BuiltinHeaderNames() {
		s.builtins[name] = true
	}
	if cfg == nil {
		return s
	}
	// FORWARD, overwriting: rules.Resolve is last-write-wins per key for
	// headers, and resolveField walks the resolved body list in REVERSE, so
	// in both cases the LAST rule naming a key is the one that fires. Two
	// rules covering the same header with different reasons would otherwise
	// have the report quote the reason that lost.
	for _, raw := range cfg.WireRules {
		for name := range raw.Headers {
			s.userHeaders[strings.ToLower(name)] = raw.Why
		}
		for glob := range raw.Body {
			s.userGlobs[glob] = raw.Why
		}
	}
	// FIRST wins here, in contrast: resolveField scans the ignore list front
	// to back and returns on the first match. Two entries for the same glob
	// are exact duplicates, and the later one never fires.
	for _, e := range cfg.WireIgnore {
		// Skipped for the same reason WireIgnorePaths drops it: an empty
		// path reaching the diff as an ignore rule would match everything,
		// so it is not a rule that can have fired.
		if e.Path == "" {
			continue
		}
		if _, seen := s.ignoreGlobs[e.Path]; !seen {
			s.ignoreGlobs[e.Path] = e.Why
		}
	}
	return s
}

// forHeader resolves a header's provenance and stated reason. The user's own
// rules are checked FIRST so that overriding a built-in reattributes the row
// to them — after `wire_rules: [{headers: {date: iso8601}}]` the tolerance
// that fired is theirs, and calling it a built-in would send them looking in
// the wrong place to change it, and quote a reason they did not write.
func (s suppressionSource) forHeader(name string) (source, why string) {
	lower := strings.ToLower(name)
	if why, ok := s.userHeaders[lower]; ok {
		return SourceWireRule, why
	}
	if s.builtins[lower] {
		return SourceBuiltin, config.BuiltinHeaderWhy(lower)
	}
	// Nothing else can tolerate a header, so a rule is the only remaining
	// explanation — reachable when cfg is nil (the config was never passed
	// down) rather than when the header is unruled. No why is available
	// there, and inventing one would be this report making something up.
	return SourceWireRule, ""
}

// forGlob resolves a body glob's provenance and stated reason, in
// resolveField's own order: rules are consulted before the wire_ignore list
// there, so a glob present in both is credited to the rule that actually
// won — and quoted with that rule's reason, not the ignore entry's.
func (s suppressionSource) forGlob(glob string) (source, why string) {
	if why, ok := s.userGlobs[glob]; ok {
		return SourceWireRule, why
	}
	if why, ok := s.ignoreGlobs[glob]; ok {
		return SourceWireIgnore, why
	}
	return SourceWireRule, ""
}

// suppressionsOf tallies every tolerance that fired across the paired
// calls. It always returns a non-nil slice — Summary.Suppressions is a
// documented array, and `null` there would read to a JSON consumer as "this
// engine does not report suppressions" rather than "none fired".
func suppressionsOf(s Summary, cfg *config.Config) []Suppression {
	src := newSuppressionSource(cfg)
	// Why is part of the KEY, not carried alongside the count. It is a pure
	// function of (source, target) as this source resolves it, so it cannot
	// actually vary within a key — including it is what makes that a fact the
	// type enforces rather than an invariant a later edit can quietly break
	// by picking one arbitrary why for a key that had two.
	type key struct{ plane, target, source, matcher, why string }
	counts := map[key]int{}

	add := func(plane, target, matcher, source, why string) {
		// An empty target would collapse unrelated tolerances into one
		// anonymous row — the most permissive reading of a value that
		// should never be empty. Today it cannot be: a FieldDiff only
		// reaches BodyTolerated/BodyIgnored when resolveField matched, and
		// matching is what sets Glob. Dropped rather than trusted, because
		// the row it would produce is worse than no row.
		if target == "" {
			return
		}
		counts[key{plane, target, source, matcher, why}]++
	}

	for _, e := range s.Wire.Paired {
		for _, hd := range e.HeaderDiff {
			if hd.Type != "tolerated" {
				continue
			}
			source, why := src.forHeader(hd.Name)
			add("header", hd.Name, hd.Matcher, source, why)
		}
		for _, hd := range e.HeaderIgnored {
			source, why := src.forHeader(hd.Name)
			add("header", hd.Name, hd.Matcher, source, why)
		}
		for _, group := range [][]FieldDiff{e.BodyTolerated, e.BodyIgnored} {
			for _, fd := range group {
				source, why := src.forGlob(fd.Glob)
				add("body", fd.Glob, fd.Matcher, source, why)
			}
		}
	}

	out := make([]Suppression, 0, len(counts))
	for k, n := range counts {
		out = append(out, Suppression{
			Plane: k.plane, Target: k.target, Source: k.source, Matcher: k.matcher, Count: n, Why: k.why,
		})
	}
	// Loudest first — the row worth reading is the rule hiding the most —
	// then a total tie-break on the remaining fields so the document is
	// byte-stable across runs with identical data. Map iteration order
	// alone is random, and a summary that reshuffles between two identical
	// runs is one that cannot be diffed.
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		if a.Plane != b.Plane {
			return a.Plane < b.Plane
		}
		if a.Target != b.Target {
			return a.Target < b.Target
		}
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		return a.Matcher < b.Matcher
	})
	return out
}
