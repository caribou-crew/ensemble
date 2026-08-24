package proxy

import (
	"regexp"
	"strings"
)

// Route is one rule of a gateway Target: requests matching it are forwarded
// to Upstream. A route matches either by Prefix (path-segment rooted,
// longest-prefix-wins) or by Regex (matched against the full path, no
// implicit anchoring — write ^ / $ yourself), never both; see resolve for
// how the two kinds combine. Prefix is "/"-rooted; a trailing slash is
// ignored (so "/cart" and "/cart/" are the same rule) except for the bare
// "/" catch-all, which matches every path.
//
// StripPrefix forwards the path with the matched prefix removed (an empty
// remainder becomes "/"); the query string is always preserved. StripPrefix
// only applies to Prefix routes — a Regex route always forwards the path
// unmodified unless Rewrite is set.
//
// Rewrite replaces the matched portion of the path instead of just
// stripping it — the piece strip_prefix can't express (e.g. /v1 forwarded
// as /internal/v1, not just /). On a Prefix route it replaces the matched
// prefix, remainder appended (mutually exclusive with StripPrefix — set by
// at most one). On a Regex route it's a regexp.ReplaceAllString template
// ($1, $2, ...) applied to the whole path, so only the matched substring
// changes and the rest of the path is untouched; empty Rewrite leaves a
// Regex route's path unmodified, as before.
type Route struct {
	Prefix      string
	Regex       *regexp.Regexp
	Upstream    string
	StripPrefix bool
	Rewrite     string
	// CORSPassthrough, when true, makes handler skip the gateway's own CORS
	// header injection and preflight short-circuit for a request matching
	// this route: OPTIONS forwards upstream like any other method, and
	// whatever the backend returns (its own CORS headers, or none) passes
	// through unmodified. For a backend that already owns its CORS (e.g. a
	// framework with CORS middleware built in) sitting behind the same
	// gateway as one that doesn't — the common "mixed backends" case a
	// single Target.CORS policy can't express. Inert (no-op, no error) on a
	// route whose gateway has no CORS policy at all.
	CORSPassthrough bool
}

// normalizePrefix drops a trailing slash from every prefix but "/".
func normalizePrefix(p string) string {
	if len(p) > 1 {
		p = strings.TrimRight(p, "/")
		if p == "" {
			p = "/"
		}
	}
	return p
}

// matchPrefix reports whether path falls under prefix on a path-segment
// boundary: "/" matches everything; otherwise the path must equal the
// prefix or continue it with a "/" (so "/cart" matches "/cart/x" but not
// "/cartoon").
func matchPrefix(prefix, path string) bool {
	if prefix == "/" {
		return true
	}
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}

// matchRoute finds the Route that would handle path, using the same
// prefix-then-regex precedence resolve applies: prefix routes first, by
// longest matching prefix; only when none match are regex routes tried, in
// declaration order, first match wins. Factored out of resolve so a caller
// that needs the route itself — not just its resolved upstream/forward,
// like handler's CORSPassthrough check — doesn't duplicate the matching
// logic.
func (t Target) matchRoute(path string) (Route, bool) {
	best := -1
	bestLen := -1
	for i, r := range t.Routes {
		if r.Prefix == "" {
			continue
		}
		p := normalizePrefix(r.Prefix)
		if matchPrefix(p, path) && len(p) > bestLen {
			best, bestLen = i, len(p)
		}
	}
	if best >= 0 {
		return t.Routes[best], true
	}
	for _, r := range t.Routes {
		if r.Regex != nil && r.Regex.MatchString(path) {
			return r, true
		}
	}
	return Route{}, false
}

// resolve picks the upstream for a request path. ok is false when no route
// matches. Only meaningful for a Target with Routes; see handler for the
// single-upstream case.
func (t Target) resolve(path string) (upstream, forward string, ok bool) {
	r, ok := t.matchRoute(path)
	if !ok {
		return "", "", false
	}
	if r.Prefix != "" {
		p := normalizePrefix(r.Prefix)
		switch {
		case r.Rewrite != "":
			remainder := strings.TrimPrefix(path, p)
			if remainder != "" && !strings.HasPrefix(remainder, "/") {
				remainder = "/" + remainder
			}
			forward = r.Rewrite + remainder
		case r.StripPrefix:
			forward = strings.TrimPrefix(path, p)
			if !strings.HasPrefix(forward, "/") {
				forward = "/" + forward
			}
		default:
			forward = path
		}
		return r.Upstream, forward, true
	}
	// Regex route.
	forward = path
	if r.Rewrite != "" {
		forward = r.Regex.ReplaceAllString(path, r.Rewrite)
	}
	return r.Upstream, forward, true
}
