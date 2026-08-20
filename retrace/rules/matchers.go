// Package rules answers one question: "these two values differ — is that
// difference tolerable?" A matcher tolerates a value change while still
// catching a SHAPE change, which is what makes it stronger than an ignore.
// Ported from the JS prototype's src/matchers.mjs + src/wire-rules.mjs;
// the regexes are deliberately byte-identical so its fixtures stay valid.
package rules

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
)

var (
	uuidRe    = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	etagRe    = regexp.MustCompile(`^(W/)?"[^"]*"$`)
	integerRe = regexp.MustCompile(`^-?\d+$`)
	semverRe  = regexp.MustCompile(`^\d+\.\d+\.\d+([-+][0-9A-Za-z.-]+)*$`)
	// Deliberately stricter than a permissive date parser, which accepts
	// "Wed" and other junk.
	isoRe      = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:?\d{2})?$`)
	httpDateRe = regexp.MustCompile(`^[A-Z][a-z]{2}, \d{2} [A-Z][a-z]{2} \d{4} \d{2}:\d{2}:\d{2} GMT$`)
)

type Kind string

const (
	KindExact   Kind = "exact"
	KindIgnore  Kind = "ignore"
	KindNamed   Kind = "named"
	KindPattern Kind = "pattern"
)

// Matcher is not a wire type — it never crosses a REST response. A rule's
// wire form is Raw (JSON/YAML), and a diff entry reports a matcher as its
// Label() string. Constructing one outside ParseMatcher (a JSON round-trip
// included, since re is unexported) is fine: Classify handles it through
// Matcher.ready() rather than assuming ParseMatcher built it.
type Matcher struct {
	Kind    Kind
	Name    string
	Pattern string
	re      *regexp.Regexp
}

func (m Matcher) Zero() bool { return m.Kind == "" }

func (m Matcher) Label() string {
	switch m.Kind {
	case KindNamed:
		return m.Name
	case KindPattern:
		return "/" + m.Pattern + "/"
	default:
		return string(m.Kind)
	}
}

var named = map[string]func(any) bool{
	"uuid":    func(v any) bool { s, ok := v.(string); return ok && uuidRe.MatchString(s) },
	"etag":    func(v any) bool { s, ok := v.(string); return ok && etagRe.MatchString(s) },
	"semver":  func(v any) bool { s, ok := v.(string); return ok && semverRe.MatchString(s) },
	"iso8601": func(v any) bool { s, ok := v.(string); return ok && isoRe.MatchString(s) && parses(s, time.RFC3339) },
	"http-date": func(v any) bool {
		s, ok := v.(string)
		return ok && httpDateRe.MatchString(s) && parses(s, http.TimeFormat)
	},
	// Accepts a JSON number too — a body field carries 1760, a header "1760".
	// Also accepts Go's own integer kinds: every value that reaches Classify
	// from encoding/json arrives as float64, but a Go-side caller (a test,
	// a future in-process consumer) may hand this a real int — accept it
	// rather than silently failing a comparison that is obviously an integer.
	"integer": func(v any) bool {
		switch t := v.(type) {
		case float64:
			return t == float64(int64(t))
		case float32:
			return float64(t) == float64(int64(t))
		case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
			return true
		case json.Number:
			_, err := t.Int64()
			return err == nil
		case string:
			return integerRe.MatchString(t)
		}
		return false
	},
}

// parses is the "is it a real date" backstop behind isoRe/httpDateRe: the
// regex is the gate (it rejects "Wed" and other junk), this only confirms
// the value parses as a real instant. It must accept every shape isoRe
// blesses — including the colon-less zone offset ("+0530") that Java, Python
// and Go's own time.Format("...Z0700") all emit — or a user who wrote an
// iso8601 rule to excuse a timestamp gets a false "violation" on exactly the
// field they excused. Layouts are generated, not hand-picked, so the set
// stays complete against isoRe's grammar: separator T or space, fractional
// seconds present or not, zone absent/"Z"/colon/colon-less.
func parses(s, layout string) bool {
	if _, err := time.Parse(layout, s); err == nil {
		return true
	}
	for _, sep := range []string{"T", " "} {
		for _, frac := range []string{"", ".999999999"} {
			for _, zone := range []string{"Z07:00", "Z0700", ""} {
				alt := "2006-01-02" + sep + "15:04:05" + frac + zone
				if _, err := time.Parse(alt, s); err == nil {
					return true
				}
			}
		}
	}
	return false
}

// Names lists every accepted matcher name, for error messages.
func Names() []string {
	out := []string{string(KindExact), string(KindIgnore)}
	rest := make([]string, 0, len(named))
	for k := range named {
		rest = append(rest, k)
	}
	sort.Strings(rest)
	return append(out, rest...)
}

// ParseMatcher accepts a name string, a {"pattern": "..."} map (either
// map[string]any from JSON/YAML or map[string]string), or nil for exact.
// An unknown name is an error rather than silently tolerating nothing.
func ParseMatcher(spec any, where string) (Matcher, error) {
	at := ""
	if where != "" {
		at = " at " + where
	}
	switch t := spec.(type) {
	case nil:
		return Matcher{Kind: KindExact}, nil
	case string:
		if t == string(KindExact) || t == string(KindIgnore) {
			return Matcher{Kind: Kind(t)}, nil
		}
		if _, ok := named[t]; ok {
			return Matcher{Kind: KindNamed, Name: t}, nil
		}
		return Matcher{}, fmt.Errorf("unknown matcher %q%s — expected one of: %s, or {pattern: ...}",
			t, at, strings.Join(Names(), ", "))
	case map[string]any:
		p, _ := t["pattern"].(string)
		return compilePattern(p, at)
	case map[string]string:
		return compilePattern(t["pattern"], at)
	}
	return Matcher{}, fmt.Errorf("invalid matcher%s — expected a name or {pattern: ...}", at)
}

// compilePattern rejects an empty pattern (the prototype's new RegExp(empty
// string) accepts it and matches everything — total silent tolerance; this
// is deliberately stricter). A non-empty pattern is compiled as-is and NOT
// auto-anchored —
// {"pattern": "v\\d+"} tolerates "xxv1yy" vs "zzv9zz" — matching the prototype's
// own new RegExp(p).test(value) exactly; anchoring is the caller's job.
func compilePattern(p, at string) (Matcher, error) {
	if p == "" {
		return Matcher{}, fmt.Errorf("invalid matcher%s — expected a name or {pattern: ...}", at)
	}
	re, err := regexp.Compile(p)
	if err != nil {
		return Matcher{}, fmt.Errorf("invalid matcher pattern %q%s: %w", p, at, err)
	}
	return Matcher{Kind: KindPattern, Pattern: p, re: re}, nil
}

func (m Matcher) satisfies(v any) bool {
	switch m.Kind {
	case KindNamed:
		return named[m.Name](v)
	case KindPattern:
		s, ok := v.(string)
		return ok && m.re.MatchString(s)
	}
	return false
}

// ready prepares m for evaluation and reports whether it can be evaluated
// at all. Classify must be total — a Matcher built any other way than
// through ParseMatcher (most commonly a JSON/YAML round-trip: Matcher's re
// field is unexported and never survives serialization) must never panic.
//
//  1. A matcher reconstructible from its exported fields is reconstructed:
//     a pattern matcher with re == nil but a non-empty Pattern recompiles,
//     so a round-tripped valid matcher keeps working instead of degrading.
//  2. A matcher that cannot be evaluated — empty or uncompilable Pattern,
//     an unknown named matcher, or an unrecognized Kind — reports false
//     readiness. Classify then answers Violation, never Ignored/Tolerated:
//     something that cannot evaluate must never be able to say "fine".
func (m Matcher) ready() (Matcher, bool) {
	switch m.Kind {
	case KindNamed:
		_, ok := named[m.Name]
		return m, ok
	case KindPattern:
		if m.re != nil {
			return m, true
		}
		if m.Pattern == "" {
			return m, false
		}
		re, err := regexp.Compile(m.Pattern)
		if err != nil {
			return m, false
		}
		m.re = re
		return m, true
	default:
		return m, false
	}
}

// Classify decides what a difference between two values means under a
// matcher. bothPresent is false when one side has no value at all: a value
// matcher cannot speak to a value that does not exist, so an appearing or
// disappearing field stays a real change — only ignore silences it.
//
// The zero Matcher (Kind == "") means "no rule applies" and always
// classifies as Changed — never Ignored, never Tolerated. That is load-
// bearing: Resolved.ForHeader/ForField return the zero Matcher when nothing
// matches, so if this ever tolerated or ignored a zero Matcher, an unruled
// field would silently stop being compared at all.
func Classify(m Matcher, a, b any, bothPresent bool) Outcome {
	switch m.Kind {
	case KindIgnore:
		return Ignored
	case "", KindExact:
		return Changed
	}
	if !bothPresent {
		return Changed
	}
	ready, ok := m.ready()
	if !ok {
		return Violation
	}
	if ready.satisfies(a) && ready.satisfies(b) {
		return Tolerated
	}
	return Violation
}

type Outcome string

const (
	Ignored   Outcome = "ignored"
	Changed   Outcome = "changed"
	Tolerated Outcome = "tolerated"
	Violation Outcome = "violation"
)
