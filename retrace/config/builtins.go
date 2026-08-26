package config

import (
	"sort"

	"github.com/caribou-crew/ensemble/retrace/rules"
)

// builtinHeaderMatchers are the header tolerances every project would
// otherwise have to write by hand before its first diff meant anything.
//
// `date` is the one that forced this. Every HTTP response carries one, so
// without a tolerance two byte-identical runs of the same suite differ on
// 100% of paired calls and any wire gate fails every single time. Measured,
// not assumed: the sample stack's browser suite reported exactly that —
// 29 of 29 paired calls changed, on nothing but the clock — until
// sample/retrace.yaml grew this rule by hand. A default a user must
// discover by watching their gate fail is a default in the wrong place.
//
// Each is a NAMED matcher, never `ignore`. The difference matters: a named
// matcher tolerates two values that both satisfy it and still reports a
// header that appears, disappears, or stops being well-formed. `ignore`
// would silence all of that. So these narrow what counts as a change; they
// do not stop the header being compared.
//
//   - date           — RFC 7231 HTTP-date, regenerated per response.
//   - etag           — an opaque validator; its whole purpose is to change
//     when the representation does, and to differ between
//     two servers that agree on the body.
//   - content-length — derived from a body this diff already compares
//     field by field. It is the one entry here that can
//     hide something on its own: a body that differs only
//     in whitespace or key order parses to identical
//     fields, and then length was the last signal left.
//     Kept anyway, because tolerating a derived value is
//     the lesser cost against failing every run that
//     tolerated a body field.
var builtinHeaderMatchers = map[string]any{
	"date":           "http-date",
	"etag":           "etag",
	"content-length": "integer",
}

// BuiltinWireRules returns the built-in rules as ordinary config rules —
// one rules.Raw per header rather than a single Raw carrying all three.
//
// One per header is what makes them individually overridable. Resolve
// collapses rules in list order with last-write-wins PER KEY, so a user
// rule for `date` beats the built-in `date` and leaves `etag` and
// `content-length` alone. Bundled into a single Raw the behaviour would be
// identical today — Resolve merges key by key either way — but the shape
// would suggest otherwise to the next reader, and to anyone who ever wants
// to attach a Method or Path to one of them.
//
// A fresh slice, and fresh maps inside it, on every call: the result is
// prepended to the user's own rules by Rules(), and a shared backing array
// there is how one config's rules end up in another's.
func BuiltinWireRules() []rules.Raw {
	names := make([]string, 0, len(builtinHeaderMatchers))
	for name := range builtinHeaderMatchers {
		names = append(names, name)
	}
	// Map iteration is random; without this the built-ins land in a
	// different order on every call. Nothing depends on their relative
	// order today (they touch disjoint keys), which is exactly why an
	// unstable order would go unnoticed until something did.
	sort.Strings(names)

	out := make([]rules.Raw, 0, len(names))
	for _, name := range names {
		out = append(out, rules.Raw{Headers: map[string]any{name: builtinHeaderMatchers[name]}})
	}
	return out
}

// BuiltinHeaderNames lists the headers BuiltinWireRules covers, sorted. The
// fired-suppression report uses it to tell a tolerance the user asked for
// from one they inherited.
func BuiltinHeaderNames() []string {
	names := make([]string, 0, len(builtinHeaderMatchers))
	for name := range builtinHeaderMatchers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// useBuiltinWireRules reports whether the built-ins apply.
//
// DefaultWireRules is a *bool so that ABSENT and an explicit `false` are
// distinguishable, and absent means ON. That is deliberately the more
// permissive reading of an unset value, against this repo's usual rule, so
// it is worth saying why: the built-ins are a specified product default,
// bounded to three named headers, and every one of them still reports an
// appearing, disappearing, or malformed header. The failure a plain `bool`
// would cause is the real hazard — its zero value is `false`, which would
// silently turn the defaults OFF for every config that never mentions
// them, and the symptom of that (a gate failing on the clock) is precisely
// what this file exists to prevent.
func (c *Config) useBuiltinWireRules() bool {
	return c.DefaultWireRules == nil || *c.DefaultWireRules
}
