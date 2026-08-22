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
// unmodified.
type Route struct {
	Prefix      string
	Regex       *regexp.Regexp
	Upstream    string
	StripPrefix bool
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

// resolve picks the upstream for a request path. Prefix routes are tried
// first, by longest matching prefix (today's behavior, unchanged). Only
// when no prefix route matches are Regex routes tried, in declaration
// order, first match wins. ok is false when no route matches. Only
// meaningful for a Target with Routes; see handler for the single-upstream
// case.
func (t Target) resolve(path string) (upstream, forward string, ok bool) {
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
		r := t.Routes[best]
		forward = path
		if r.StripPrefix {
			forward = strings.TrimPrefix(path, normalizePrefix(r.Prefix))
			if !strings.HasPrefix(forward, "/") {
				forward = "/" + forward
			}
		}
		return r.Upstream, forward, true
	}
	for _, r := range t.Routes {
		if r.Regex != nil && r.Regex.MatchString(path) {
			return r.Upstream, path, true
		}
	}
	return "", "", false
}
