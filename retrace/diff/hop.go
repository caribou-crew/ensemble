// hop.go diffs two runs' hop trees: which downstream services got called
// how many times, which routes are new or gone, which error signatures
// are new or gone, and whether a configured hopRequire assertion still
// holds. Ported in spirit from the prototype's src/hop-diff.mjs, with the same
// coarseness: a run's own retry/poll cadence is not reproducible, so hops
// are never paired call-for-call the way wire.jsonl is — this file
// compares aggregates (counts, route sets, error signatures) instead.
package diff

import (
	"math"
	"sort"
	"strconv"

	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/retrace/config"
)

// DefaultCountTolerance is the fraction of drift (relative to the larger
// of the two counts) a service's call count may move by before
// ServiceCount.Deviates flags it, when HopOptions.CountTolerance is left
// at its zero value.
const DefaultCountTolerance = 0.5

// ServiceCount is how many logical calls each run made to one downstream
// service (LogicalHop.Origin.To), after relay folding.
type ServiceCount struct {
	Service  string `json:"service"`
	A        int    `json:"a"`
	B        int    `json:"b"`
	Deviates bool   `json:"deviates"`
}

// Route is one call identity: the destination service, method and
// (normalized) path. Via lists any relays CollapseRelays folded out of
// the middle to produce it, in order; empty when nothing folded.
type Route struct {
	To     string   `json:"to"`
	Method string   `json:"method"`
	Path   string   `json:"path"`
	Via    []string `json:"via,omitempty"`
}

// RouteFailure is one config.RequiredRoute that RequiredRouteFailures
// could not confirm against side B: either no hop matched Method+Path at
// all ("missing"), or one did but never with the required Status
// ("wrong-status", carrying the status that was actually seen).
type RouteFailure struct {
	Method         string `json:"method"`
	Path           string `json:"path"`
	ExpectedStatus int    `json:"expectedStatus"`
	ActualStatus   int    `json:"actualStatus"`
	Reason         string `json:"reason"`
}

// HopDiff is DiffHops' result.
type HopDiff struct {
	ServiceCounts []ServiceCount `json:"serviceCounts"`
	// NewErrors/GoneErrors are error signatures (method, normalized path,
	// status, deduped to one entry each) present on one side of the raw
	// hops and not the other, excluding any status HopOptions.Expected
	// excuses — an allowlisted 404 appearing for the first time on side B
	// is not a regression signal, it is the rule doing its job.
	NewErrors             []StatusFinding `json:"newErrors,omitempty"`
	GoneErrors            []StatusFinding `json:"goneErrors,omitempty"`
	NewRoutes             []Route         `json:"newRoutes"`
	GoneRoutes            []Route         `json:"goneRoutes"`
	RequiredRouteFailures []RouteFailure  `json:"requiredFailures,omitempty"`
	// HopRequireConfigured distinguishes "no hopRequire entries were
	// configured" from "hopRequire entries were configured and all
	// passed" — both leave RequiredRouteFailures empty, and a consumer
	// that can't tell them apart can't tell "nothing to check" from "gate
	// green".
	HopRequireConfigured bool `json:"hopRequireConfigured"`
}

// HopOptions configures one DiffHops call.
type HopOptions struct {
	Normalize func(string) string
	Expected  []config.StatusRule
	Require   []config.RequiredRoute
	// CountTolerance zero means "unset" and falls back to
	// DefaultCountTolerance — a caller that wants NO tolerance passes a
	// negative value, which is clamped to 0 (any nonzero drift flags).
	// Stated because a zero-value field that silently means something
	// other than zero is exactly how a "0% tolerance" intent becomes 50%.
	CountTolerance float64
	// NoCollapse turns relay folding OFF. The field is negative on
	// purpose: folding is the wanted behaviour on every real run, and a
	// `Collapse bool` documented as "default true" is a documentation
	// claim a bool cannot keep — its zero value is false, so every caller
	// that built HopOptions without naming the field would get folding
	// OFF and every relay topology change would read as a new API call.
	// That is precisely the false positive this task exists to prevent,
	// and its own test would still have passed, because the test sets
	// the field explicitly.
	//
	// No pointer, no sentinel: the zero value IS the default, which is
	// the only shape that cannot be got wrong by omission.
	NoCollapse bool
}

// routesFromLogicalHops lowers folded hops into the route identities the
// diff compares. Origin carries the outcome (destination, status); Hop
// carries the request identity (method, path). Mixing them up is silent
// — the route set stays the same size and every name is wrong.
func routesFromLogicalHops(lhs []trace.LogicalHop, normalize func(string) string) []Route {
	seen := map[string]bool{}
	var out []Route
	for _, lh := range lhs {
		path := lh.Hop.Path
		if normalize != nil {
			path = normalize(path)
		}
		r := Route{
			To:     lh.Origin.To, // NOT lh.Hop.To — see the doc comment above.
			Method: lh.Hop.Method,
			Path:   path,
			Via:    lh.Via,
		}
		key := routeKey(r.To, r.Method, r.Path)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, r)
	}
	return out
}

func routeKey(to, method, path string) string {
	return to + " " + method + " " + path
}

// CollapsedRoutes folds hops (relay collapse always on) and lowers them
// into the routes the diff compares — the same lowering DiffHops applies
// to each side, exposed standalone for a caller (a review UI) that wants
// one side's route set without diffing.
func CollapsedRoutes(hops []trace.Hop, normalize func(string) string) []Route {
	return routesFromLogicalHops(trace.CollapseRelays(hops, true), normalize)
}

func diffRoutes(a, b []Route) (newRoutes, goneRoutes []Route) {
	inA := map[string]bool{}
	for _, r := range a {
		inA[routeKey(r.To, r.Method, r.Path)] = true
	}
	inB := map[string]bool{}
	for _, r := range b {
		inB[routeKey(r.To, r.Method, r.Path)] = true
	}
	for _, r := range b {
		if !inA[routeKey(r.To, r.Method, r.Path)] {
			newRoutes = append(newRoutes, r)
		}
	}
	for _, r := range a {
		if !inB[routeKey(r.To, r.Method, r.Path)] {
			goneRoutes = append(goneRoutes, r)
		}
	}
	return newRoutes, goneRoutes
}

// serviceCountsFromLogical counts LogicalHop.Origin.To on each side (same
// reason as routesFromLogicalHops: Origin carries the real destination)
// and flags a service whose count moved by more than tol of the larger
// side's count.
func serviceCountsFromLogical(la, lb []trace.LogicalHop, tol float64) []ServiceCount {
	countsA := map[string]int{}
	for _, lh := range la {
		countsA[lh.Origin.To]++
	}
	countsB := map[string]int{}
	for _, lh := range lb {
		countsB[lh.Origin.To]++
	}
	services := map[string]bool{}
	for s := range countsA {
		services[s] = true
	}
	for s := range countsB {
		services[s] = true
	}
	names := make([]string, 0, len(services))
	for s := range services {
		names = append(names, s)
	}
	sort.Strings(names)

	out := make([]ServiceCount, 0, len(names))
	for _, s := range names {
		a, b := countsA[s], countsB[s]
		out = append(out, ServiceCount{Service: s, A: a, B: b, Deviates: countDeviates(a, b, tol)})
	}
	return out
}

func countDeviates(a, b int, tol float64) bool {
	if a == b {
		return false
	}
	base := math.Max(float64(a), float64(b))
	if base == 0 {
		return false
	}
	ratio := math.Abs(float64(a-b)) / base
	return ratio > tol
}

// errorSignature is the dedup key for one (method, normalized path,
// status) error: FindUnexpectedStatuses reports every occurrence,
// error-signature diffing reports whether the SIGNATURE is new or gone,
// once, no matter how many hops on that side share it.
func errorSignatures(hops []trace.Hop, normalize func(string) string, expected []config.StatusRule) map[string]StatusFinding {
	out := map[string]StatusFinding{}
	for _, h := range hops {
		if h.Status/100 != 4 && h.Status/100 != 5 {
			continue
		}
		if isExcused(h, expected) {
			continue
		}
		path := h.Path
		if normalize != nil {
			path = normalize(path)
		}
		key := h.Method + " " + path + " " + strconv.Itoa(h.Status)
		if _, ok := out[key]; !ok {
			out[key] = StatusFinding{Seq: h.Seq, Method: h.Method, Path: path, Status: h.Status}
		}
	}
	return out
}

func diffErrorSignatures(a, b []trace.Hop, normalize func(string) string, expected []config.StatusRule) (newErrors, goneErrors []StatusFinding) {
	sigA := errorSignatures(a, normalize, expected)
	sigB := errorSignatures(b, normalize, expected)

	var newKeys []string
	for k := range sigB {
		if _, ok := sigA[k]; !ok {
			newKeys = append(newKeys, k)
		}
	}
	sort.Strings(newKeys)
	for _, k := range newKeys {
		newErrors = append(newErrors, sigB[k])
	}

	var goneKeys []string
	for k := range sigA {
		if _, ok := sigB[k]; !ok {
			goneKeys = append(goneKeys, k)
		}
	}
	sort.Strings(goneKeys)
	for _, k := range goneKeys {
		goneErrors = append(goneErrors, sigA[k])
	}
	return newErrors, goneErrors
}

// RequiredRouteFailures takes ONE hop slice, and the caller contract is
// that it is side B, RAW (uncollapsed) — a hard gate must be evaluated
// against what actually happened on the candidate, not against a folded
// view of it and not against the reference. DiffHops calls it with b.
//
// Path matching uses MatchURLGlob (the same dialect config.StatusRule
// uses), not literal equality: a required route named with a template
// segment ("/api/orders/*") should assert against every order id, not one
// specific literal path frozen at config-write time. A brief that only
// spelled out {Method, Path, Status} left this open; MatchURLGlob is the
// existing tool for "a configured route pattern against a captured path"
// and a literal path with no '*' matches under it exactly as it would
// under ==, so this is a strict superset of literal matching, never a
// narrowing.
func RequiredRouteFailures(hopsB []trace.Hop, require []config.RequiredRoute) []RouteFailure {
	var out []RouteFailure
	for _, req := range require {
		matched := false
		sawRoute := false
		actual := 0
		for _, h := range hopsB {
			if h.Method != req.Method || !MatchURLGlob(req.Path, h.Path) {
				continue
			}
			sawRoute = true
			if h.Status == req.Status {
				matched = true
				break
			}
			actual = h.Status
		}
		if matched {
			continue
		}
		if sawRoute {
			out = append(out, RouteFailure{
				Method: req.Method, Path: req.Path,
				ExpectedStatus: req.Status, ActualStatus: actual,
				Reason: "wrong-status",
			})
			continue
		}
		out = append(out, RouteFailure{
			Method: req.Method, Path: req.Path,
			ExpectedStatus: req.Status,
			Reason:         "missing",
		})
	}
	return out
}

// DiffHops folds both sides with trace.CollapseRelays(hops, !o.NoCollapse)
// before deriving service counts and routes, and each Route carries the
// folded relays in Via. RequiredRouteFailures and error signatures run
// against the RAW hops — a hopRequire assertion names a real route and a
// 500 is a 500 no matter who relayed it.
func DiffHops(a, b []trace.Hop, o HopOptions) HopDiff {
	tol := o.CountTolerance
	switch {
	case tol == 0:
		tol = DefaultCountTolerance
	case tol < 0:
		tol = 0
	}

	collapse := !o.NoCollapse
	la := trace.CollapseRelays(a, collapse)
	lb := trace.CollapseRelays(b, collapse)

	routesA := routesFromLogicalHops(la, o.Normalize)
	routesB := routesFromLogicalHops(lb, o.Normalize)
	newRoutes, goneRoutes := diffRoutes(routesA, routesB)

	serviceCounts := serviceCountsFromLogical(la, lb, tol)

	newErrors, goneErrors := diffErrorSignatures(a, b, o.Normalize, o.Expected)

	configured := len(o.Require) > 0
	var reqFailures []RouteFailure
	if configured {
		reqFailures = RequiredRouteFailures(b, o.Require)
	}

	return HopDiff{
		ServiceCounts:         serviceCounts,
		NewErrors:             newErrors,
		GoneErrors:            goneErrors,
		NewRoutes:             newRoutes,
		GoneRoutes:            goneRoutes,
		RequiredRouteFailures: reqFailures,
		HopRequireConfigured:  configured,
	}
}
