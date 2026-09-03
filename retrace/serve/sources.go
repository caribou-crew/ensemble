package serve

import (
	"fmt"
	"maps"
	"sort"

	"github.com/caribou-crew/ensemble/retrace/config"
)

// NewDepsForRoot builds the Deps a `retrace serve` instance would have for
// one project root — the same config.Discover(cwd) + Deps{...} 	construction
// `retrace/cmd/retrace/cmd_serve.go` has always done inline, factored out
// here so a caller building more than one Deps (one per
// retrace.repo.yaml-mapped root) does not repeat it per root.
func NewDepsForRoot(root string, allowedHosts []string, version string) (Deps, error) {
	cfg, err := config.Discover(root)
	if err != nil {
		return Deps{}, err
	}
	return Deps{Cwd: root, Cfg: cfg, AllowedHosts: allowedHosts, Version: version}, nil
}

// Sources aggregates one Deps per distinct project root, plus the app-key
// -> root map that resolves an {app} path value to the Deps that governs
// it. It is what lets `retrace serve` show every app a retrace.repo.yaml
// maps in one dashboard/API, regardless of which root each app's runs
// live under (design.md D3) — BuildQueue and the per-flow handlers this
// package already has are UNCHANGED; Sources composes them per root
// rather than replacing them.
//
// Sources is treated as immutable once built: reloading one root's config
// (see server.reloadConfig) builds a NEW Sources value with that one
// root's Deps swapped in, rather than mutating byRoot in place — the same
// "swap the pointer, never mutate what a concurrent reader might already
// be holding" discipline server.d already follows (see server's own doc
// comment).
type Sources struct {
	byRoot  map[string]Deps
	appRoot map[string]string
}

// NewSources builds a Sources from one Deps per distinct root
// (byRoot, keyed by Deps.Cwd) and an app-key -> root-directory map — the
// shape repoconfig.Config.Apps naturally produces (one entry per app key,
// several keys may share one root). Every value in appRoot MUST have a
// matching entry in byRoot; NewSources returns an error naming the
// offending app/root pair otherwise, since a Sources that could not
// resolve its own map would fail confusingly and only on first request.
func NewSources(byRoot map[string]Deps, appRoot map[string]string) (Sources, error) {
	for app, root := range appRoot {
		if _, ok := byRoot[root]; !ok {
			return Sources{}, fmt.Errorf("serve: app %q maps to root %q, which has no Deps built for it", app, root)
		}
	}
	return Sources{byRoot: byRoot, appRoot: appRoot}, nil
}

// DepsFor resolves app to the Deps governing it, and reports whether app
// is a key in the underlying map at all. An app absent from the map is
// NOT an error here — see server.depsForApp, which falls back to the
// server's own default Deps for exactly this case, matching how an app
// with no ensemble RetraceConfig.Apps entry falls back to that stack's
// one Cfg today.
func (s Sources) DepsFor(app string) (Deps, bool) {
	root, ok := s.appRoot[app]
	if !ok {
		return Deps{}, false
	}
	d, ok := s.byRoot[root]
	return d, ok
}

// Roots returns every distinct root's Deps, sorted by Cwd for a
// deterministic build order (BuildQueue's own worst-first sort makes the
// final ORDER deterministic regardless, but a deterministic build order
// keeps a broken-root error naming the same offender across runs).
func (s Sources) Roots() []Deps {
	out := make([]Deps, 0, len(s.byRoot))
	for _, d := range s.byRoot {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Cwd < out[j].Cwd })
	return out
}

// RootTargets returns each distinct root directory paired with the app
// keys mapped to it, sorted by root for determinism. It is the on-demand
// sync analog of the per-root watchTarget list cmd_serve.go builds for
// `--watch`: a sync must run once per root with that root's Cwd and app
// allowlist, so each app's runs land under the root holding its own
// .retrace-ref/ (design.md D4) rather than all under the serve process's
// single cwd — which would orphan every app whose reference lives in a
// different root, quarantining it. Apps are the same allowlist
// repoconfig.Config.AppsIn produces; a root with no apps is omitted.
func (s Sources) RootTargets() []RootTarget {
	byRoot := make(map[string][]string, len(s.byRoot))
	for app, root := range s.appRoot {
		byRoot[root] = append(byRoot[root], app)
	}
	out := make([]RootTarget, 0, len(byRoot))
	for root, apps := range byRoot {
		sort.Strings(apps)
		out = append(out, RootTarget{Root: root, Apps: apps})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Root < out[j].Root })
	return out
}

// RootTarget is one project root plus the app keys mapped to it — what a
// per-root on-demand sync needs to write each app's runs under the root
// holding its own reference. See Sources.RootTargets.
type RootTarget struct {
	Root string
	Apps []string
}

// withConfig returns a Sources identical to s except that root's Deps
// carries cfg instead of its current Cfg — see Sources' own doc comment
// for why this returns a new value rather than mutating s.byRoot in
// place. A root not present in s.byRoot is returned unchanged; the caller
// (server.reloadConfig) only ever calls this with a root it already
// resolved a request's Deps from, so that root is always present.
func (s Sources) withConfig(root string, cfg *config.Config) Sources {
	byRoot := make(map[string]Deps, len(s.byRoot))
	maps.Copy(byRoot, s.byRoot)
	if d, ok := byRoot[root]; ok {
		d.Cfg = cfg
		byRoot[root] = d
	}
	return Sources{byRoot: byRoot, appRoot: s.appRoot}
}

// BuildQueue aggregates the review queue across every root: the existing,
// unmodified per-root BuildQueue is called once per root, and the
// combined items are re-sorted with the exact same worst-first comparator
// single-root BuildQueue already uses (sortItems) — so a Sources built
// from exactly one root produces byte-identical output to calling
// BuildQueue on that root's Deps directly.
func (s Sources) BuildQueue() ([]Item, error) {
	var items []Item
	for _, d := range s.Roots() {
		rootItems, err := BuildQueue(d)
		if err != nil {
			return nil, fmt.Errorf("serve: building the queue for %s: %w", d.Cwd, err)
		}
		items = append(items, rootItems...)
	}
	if items == nil {
		items = []Item{}
	}
	sortItems(items)
	return items, nil
}
