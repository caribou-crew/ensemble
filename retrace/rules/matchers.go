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
	"integer": func(v any) bool {
		switch t := v.(type) {
		case float64:
			return t == float64(int64(t))
		case json.Number:
			_, err := t.Int64()
			return err == nil
		case string:
			return integerRe.MatchString(t)
		}
		return false
	},
}

func parses(s, layout string) bool {
	if _, err := time.Parse(layout, s); err == nil {
		return true
	}
	// RFC3339 without a zone, and the "YYYY-MM-DD HH:MM:SS" variant the
	// regex already blessed, still parse — the regex is the gate, this is
	// only the "is it a real date" backstop.
	for _, alt := range []string{"2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02T15:04:05.999999999Z07:00"} {
		if _, err := time.Parse(alt, s); err == nil {
			return true
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

// Classify decides what a difference between two values means under a
// matcher. bothPresent is false when one side has no value at all: a value
// matcher cannot speak to a value that does not exist, so an appearing or
// disappearing field stays a real change — only ignore silences it.
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
	if m.satisfies(a) && m.satisfies(b) {
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
