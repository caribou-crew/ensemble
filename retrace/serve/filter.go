package serve

import (
	"net/url"
	"strings"
)

// QueueFilter narrows the review queue to the rows a reviewer asked for. It
// answers two questions the flat worst-first queue could not: "show me only
// my local runs" vs "only what CI recorded", and "only this one app" vs
// "everything". Both are read off data the Item already carries —
// Item.Source (nil == local, set == a `retrace sync` CI merge) and Item.App
// — so filtering adds no diff or capture work; it only hides rows the
// reviewer said they don't want to see.
//
// The zero QueueFilter matches everything, so an unfiltered request behaves
// exactly as before this type existed.
type QueueFilter struct {
	// Source is "", "local", or "ci". "" matches both.
	Source string
	// App, when set, matches one exact surface key — App is whatever a
	// project's own retrace config names it (a single app, or one entry of
	// a repo.yaml `apps:` map). There is no further structure to it: the
	// naming convention inside an app key belongs to the project, not to
	// retrace, so this filter only ever does an exact match.
	App string
}

// QueueFilterFromQuery reads a QueueFilter off a request's query string. All
// keys are optional; unknown values are kept verbatim and simply match
// nothing, so a typo narrows to an empty queue rather than silently matching
// everything (a filter that quietly does nothing is the more dangerous
// failure on a review surface).
func QueueFilterFromQuery(q url.Values) QueueFilter {
	return QueueFilter{
		Source: strings.ToLower(strings.TrimSpace(q.Get("source"))),
		App:    strings.TrimSpace(q.Get("app")),
	}
}

// empty reports whether this filter narrows nothing — the fast path that
// lets Apply skip a full copy of the slice for the common unfiltered
// request.
func (f QueueFilter) empty() bool {
	return f.Source == "" && f.App == ""
}

// sourceLabel folds an Item's provenance into the filter vocabulary: a nil
// Source is a locally recorded run (see runs.Source's doc — absence is the
// encoding of "local"), any non-nil Source is a CI sync.
func sourceLabel(it Item) string {
	if it.Source == nil {
		return "local"
	}
	return "ci"
}

// match reports whether one item passes the filter. Each set field must
// match; unset fields match anything.
func (f QueueFilter) match(it Item) bool {
	if f.Source != "" && sourceLabel(it) != f.Source {
		return false
	}
	if f.App != "" && it.App != f.App {
		return false
	}
	return true
}

// Apply returns the items that pass the filter, preserving order. It never
// returns nil (an empty result is []Item{}), so the queue response encodes
// as [] and the EmptyReasonFor contract on the caller side is unchanged.
func (f QueueFilter) Apply(items []Item) []Item {
	if f.empty() {
		return items
	}
	out := make([]Item, 0, len(items))
	for _, it := range items {
		if f.match(it) {
			out = append(out, it)
		}
	}
	return out
}
