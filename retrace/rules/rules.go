package rules

import (
	"fmt"
	"sort"
	"strings"
)

type Raw struct {
	Method  string         `json:"method,omitempty"  yaml:"method"`
	Path    string         `json:"path,omitempty"    yaml:"path"`
	Headers map[string]any `json:"headers,omitempty" yaml:"headers"`
	Body    map[string]any `json:"body,omitempty"    yaml:"body"`
	// Why explains what this rule is tolerating and why it is allowed to
	// change. A wire rule silences a difference; an un-explained one is
	// indistinguishable from a rule added to make a failing build go away,
	// and rules outlive the person who added them. Optional by default and
	// omitempty on the JSON side so every existing config and overlay keeps
	// loading; `require_why: true` makes it mandatory (config.ValidateWhy).
	//
	// It is per-RAW rather than per-header/per-glob because that is the
	// granularity the config has: one Raw is one authored decision, however
	// many headers or globs it names. A Raw covering two unrelated fields
	// under one reason is a signal to split it, not a reason to widen this.
	//
	// Normalize does not read it — Why is documentation, never matching —
	// so it deliberately does not appear on Rule. The report reads it back
	// off the config (diff.suppressionSource), which is also why a rule
	// whose Why changes is a different rule to AppendWireRule's idempotency
	// check: same match, different stated reason, and the newer reason is
	// worth keeping.
	Why string `json:"why,omitempty" yaml:"why"`
	// Exclude drops every matching exchange from a reference bundle at
	// load (replay.LoadBundle) — the escape hatch for an exchange the
	// loader would otherwise refuse the whole bundle over (a truncated
	// body, Content-Encoding, a 206 partial). A live request matching the
	// excluded route then misses with the standard explained 501: the
	// exchange is out of the contract, never answered wrong. Unlike Why on
	// an ordinary rule, an exclude's Why is MANDATORY regardless of
	// require_why — dropping a recorded exchange is the strongest tolerance
	// there is, and Normalize refuses one with no stated reason.
	Exclude bool `json:"exclude,omitempty" yaml:"exclude"`
}

// BodyRule and Rule are not wire types — like Matcher, they never cross a
// REST response; only Raw does (see Raw's json/yaml tags). Rule.Method is
// stored upper-cased by Normalize; Resolve upper-cases the incoming method
// too, so lookup is case-insensitive on both sides.
type BodyRule struct {
	Glob    string
	Matcher Matcher
}

type Rule struct {
	Method  string // "" = any; stored upper-cased
	Path    string // "" = any
	Headers map[string]Matcher
	Body    []BodyRule
	Exclude bool // see Raw.Exclude
}

// Normalize validates and lowers a config's raw rules. Map iteration order
// is random in Go, so header and body entries within ONE raw rule are
// sorted by key — otherwise "last one wins" would be nondeterministic
// between runs of the same config. Sorting is alphabetical, which means
// precedence among two overlapping globs in the SAME raw rule is
// alphabetical too, not authorship order: "*" (0x2A) sorts below every
// letter and digit, so a wildcard-leading glob sorts first and a more
// specific glob written before it in the config still wins the last-write
// race. Rules split across separate Raw entries are unaffected — those are
// resolved in the caller's list order by Resolve, not by this sort.
func Normalize(raw []Raw) ([]Rule, error) {
	out := make([]Rule, 0, len(raw))
	for i, r := range raw {
		where := fmt.Sprintf("wireRules[%d]", i)
		if r.Exclude && strings.TrimSpace(r.Why) == "" {
			return nil, fmt.Errorf("%s: exclude: true requires a why — dropping a recorded exchange from the reference is the strongest tolerance there is, and an unexplained one is indistinguishable from a rule added to make a failing load go away", where)
		}
		rule := Rule{Method: strings.ToUpper(r.Method), Path: r.Path, Headers: map[string]Matcher{}, Exclude: r.Exclude}
		for _, name := range sortedKeys(r.Headers) {
			m, err := ParseMatcher(r.Headers[name], where+".headers."+name)
			if err != nil {
				return nil, err
			}
			rule.Headers[strings.ToLower(name)] = m
		}
		for _, glob := range sortedKeys(r.Body) {
			m, err := ParseMatcher(r.Body[glob], where+".body."+glob)
			if err != nil {
				return nil, err
			}
			rule.Body = append(rule.Body, BodyRule{Glob: glob, Matcher: m})
		}
		out = append(out, rule)
	}
	return out, nil
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Resolved is not a wire type either — see Rule's comment. ForHeader and
// ForField both return the zero Matcher (Zero() == true) when nothing
// applies; that zero classifies as Changed under Classify, never Ignored —
// an unruled field is compared normally, not silently excused.
type Resolved struct {
	Headers map[string]Matcher
	Body    []BodyRule
}

// Resolve collapses every rule that applies to this call into one lookup.
// Later rules overwrite earlier ones per key, so a specific rule placed
// after a global one wins for that key alone.
func Resolve(rs []Rule, method, normalizedPath string) Resolved {
	res := Resolved{Headers: map[string]Matcher{}}
	for _, r := range rs {
		if r.Method != "" && r.Method != strings.ToUpper(method) {
			continue
		}
		if !MatchPathGlob(r.Path, normalizedPath) {
			continue
		}
		for k, m := range r.Headers {
			res.Headers[k] = m
		}
		res.Body = append(res.Body, r.Body...)
	}
	return res
}

func (r Resolved) ForHeader(name string) Matcher { return r.Headers[strings.ToLower(name)] }

// ForField mirrors the header map's last-write-wins: the last matching body
// glob decides.
func (r Resolved) ForField(fieldPath string) Matcher {
	for i := len(r.Body) - 1; i >= 0; i-- {
		if MatchFieldGlob(r.Body[i].Glob, fieldPath) {
			return r.Body[i].Matcher
		}
	}
	return Matcher{}
}

// MatchPathGlob matches a '/'-segmented URL path. '*' matches within one
// segment (so both "/experience/*" and "/experience/*.json" work); '**'
// spans any number of segments, including none. An empty glob matches
// everything — an unscoped rule applies to every call.
func MatchPathGlob(glob, path string) bool {
	if glob == "" {
		return true
	}
	return matchSegs(split(glob, "/"), split(path, "/"))
}

// MatchFieldGlob is the same algorithm over '.'-segmented JSON field paths.
// Array indices are part of their segment ("items[0]"), so "items[*]" is
// spelled "items.*" only when the walker emits index segments — see
// retrace/diff/wire.go, which emits "items[0].sku".
//
// Unlike MatchPathGlob, this does NOT filter out empty segments — matching
// the prototype's matchesFieldGlob, which splits on '.' without a filter (only
// its path splitter filters). So "a.b" does not match the field path
// "a..b" (an empty middle segment is real structure, e.g. an empty-string
// JSON key), while "a.*.b" does, since '*' matches any one segment
// including an empty one. This also makes MatchFieldGlob("", x) false where
// MatchPathGlob("", x) is true — deliberate: Rule.Path uses "" to mean "any
// path" (a Go string cannot be null, unlike the prototype's JS), but a body glob
// is a map key, where "" is a legal literal glob rather than "unset".
func MatchFieldGlob(glob, fieldPath string) bool {
	var pathSegs []string
	if fieldPath != "" {
		pathSegs = strings.Split(fieldPath, ".")
	}
	return matchSegs(strings.Split(glob, "."), pathSegs)
}

func split(s, sep string) []string {
	out := []string{}
	for _, p := range strings.Split(s, sep) {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func matchSegs(pattern, path []string) bool {
	if len(pattern) == 0 {
		return len(path) == 0
	}
	head := pattern[0]
	if head == "**" {
		for i := 0; i <= len(path); i++ {
			if matchSegs(pattern[1:], path[i:]) {
				return true
			}
		}
		return false
	}
	if len(path) == 0 {
		return false
	}
	if !segMatches(head, path[0]) {
		return false
	}
	return matchSegs(pattern[1:], path[1:])
}

// segMatches handles '*' inside one segment without regexp compilation per
// call: the pattern is split on '*' and matched as an ordered set of
// literal chunks.
func segMatches(pattern, seg string) bool {
	if pattern == "*" {
		return true
	}
	if !strings.Contains(pattern, "*") {
		return pattern == seg
	}
	parts := strings.Split(pattern, "*")
	if !strings.HasPrefix(seg, parts[0]) {
		return false
	}
	rest := seg[len(parts[0]):]
	for i := 1; i < len(parts)-1; i++ {
		idx := strings.Index(rest, parts[i])
		if idx < 0 {
			return false
		}
		rest = rest[idx+len(parts[i]):]
	}
	last := parts[len(parts)-1]
	return strings.HasSuffix(rest, last) && len(rest) >= len(last)
}
