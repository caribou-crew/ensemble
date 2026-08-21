// status.go finds hops whose status the retrace.yaml expected-status
// allowlist does not excuse — see config.StatusRule. The glob matcher here
// (MatchURLGlob) is deliberately a different dialect from
// rules.MatchFieldGlob: that one walks dot-separated body-field paths,
// this one walks '/'-separated URL paths, and a URL carries a query string
// a body field path never does.
package diff

import (
	"strings"

	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/retrace/config"
)

// StatusFinding is one hop whose status was not excused — by
// FindUnexpectedStatuses against config.StatusRule, or, deduped one per
// route+status, as a NewError/GoneError inside HopDiff.
type StatusFinding struct {
	Seq    uint64 `json:"seq"`
	Method string `json:"method"`
	Path   string `json:"path"`
	Status int    `json:"status"`
}

// splitURLPath splits a URL path on '/', dropping empty segments so a
// leading, trailing or doubled slash doesn't fabricate an empty segment
// that would never match a literal or a glob.
func splitURLPath(p string) []string {
	var out []string
	for _, seg := range strings.Split(p, "/") {
		if seg != "" {
			out = append(out, seg)
		}
	}
	return out
}

// stripQueryAndFragment removes a trailing '?...' or '#...' from a URL
// path. A rule is written against the route, not any particular run's
// query parameters, so matching the raw path (with "?fresh=1" still
// attached) would silently un-excuse an otherwise-expected status on every
// run whose cache-buster or pagination param happened to differ.
func stripQueryAndFragment(p string) string {
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		return p[:i]
	}
	return p
}

// MatchURLGlob reports whether pattern matches urlPath, segment by
// segment on '/'. Within one segment, '*' matches any run of characters
// (including a whole segment, as in "/api/cards/*/eligibility"). A
// pattern segment of exactly "**" backtracks over any span of path
// segments, including zero, so it can span segment boundaries the way a
// single '*' cannot. The query string and fragment are stripped from
// urlPath before matching; pattern is matched as given.
func MatchURLGlob(pattern, urlPath string) bool {
	return matchURLSegs(splitURLPath(pattern), splitURLPath(stripQueryAndFragment(urlPath)))
}

func matchURLSegs(pattern, path []string) bool {
	if len(pattern) == 0 {
		return len(path) == 0
	}
	head := pattern[0]
	if head == "**" {
		for i := 0; i <= len(path); i++ {
			if matchURLSegs(pattern[1:], path[i:]) {
				return true
			}
		}
		return false
	}
	if len(path) == 0 {
		return false
	}
	if !urlSegMatches(head, path[0]) {
		return false
	}
	return matchURLSegs(pattern[1:], path[1:])
}

// urlSegMatches handles '*' inside one URL segment without compiling a
// regexp per call: the pattern is split on '*' and matched as an ordered
// set of literal chunks, same technique as rules.MatchFieldGlob's
// segMatches.
func urlSegMatches(pattern, seg string) bool {
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

// isExcused reports whether hop h's status is allowlisted by any expected
// rule: same status, and the rule's path glob matches h's path (query
// stripped).
func isExcused(h trace.Hop, expected []config.StatusRule) bool {
	for _, e := range expected {
		if e.Status == h.Status && MatchURLGlob(e.Path, h.Path) {
			return true
		}
	}
	return false
}

// FindUnexpectedStatuses reports every hop whose status is 4xx or 5xx and
// is not excused by an entry in expected. A hop that carries no status at
// all — a transport error, which records its failure in Err instead — is
// never a finding here; CheckPerfBudget and the wire plane are where a
// dropped connection shows up.
func FindUnexpectedStatuses(hops []trace.Hop, expected []config.StatusRule) []StatusFinding {
	var out []StatusFinding
	for _, h := range hops {
		if h.Status == 0 {
			continue
		}
		if h.Status/100 != 4 && h.Status/100 != 5 {
			continue
		}
		if isExcused(h, expected) {
			continue
		}
		out = append(out, StatusFinding{Seq: h.Seq, Method: h.Method, Path: h.Path, Status: h.Status})
	}
	return out
}
