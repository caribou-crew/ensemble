// Package serve is retrace's review queue: the worst-first list of what
// changed across every recorded flow, and the REST surface a human (through
// the UI) or an agent walks it with. Every verb the UI offers is a REST call
// an agent can make identically, with the same effect — there is no
// second implementation of accept, reject or rule anywhere in this package;
// each one delegates to refs/config exactly as `retrace ref` does.
package serve

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/caribou-crew/ensemble/retrace/config"
	"github.com/caribou-crew/ensemble/retrace/diff"
	"github.com/caribou-crew/ensemble/retrace/refs"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

// Item is one flow's line in the review queue. Every field crosses the wire
// (global-constraints.md), so every one carries an explicit camelCase tag.
type Item struct {
	App      string             `json:"app"`
	Flow     string             `json:"flow"`
	Verdict  string             `json:"verdict"` // diff.Summary.Verdict
	Score    float64            `json:"score"`   // worst-first sort key
	RunID    string             `json:"runId"`
	RefRunID string             `json:"refRunId,omitempty"`
	Counts   diff.Counts        `json:"counts"`
	Capture  diff.CaptureBanner `json:"capture"`
	// Gates is never omitempty, and itemGates never returns nil (R-W).
	// Summary.Gates — the same field name, one route over on the same REST
	// surface — carries a bare tag like every other array field on that
	// type, and two opposite presence contracts for one name is a
	// distinction no consumer can be expected to remember. A row with no
	// gates is the HEALTHY row, so the omitted key landed on exactly the
	// rows a queue screen renders most of: `item.gates.length` is undefined
	// there, and that throws synchronously inside the render.
	Gates []string `json:"gates"`
}

// Deps is everything the queue and its handlers need. It is a value, copied
// per request from the server's current state, so a config reload (POST
// .../rule) swaps one pointer under a lock instead of mutating a Config
// another request is already reading.
type Deps struct {
	Cwd          string
	Cfg          *config.Config
	AllowedHosts []string
	Version      string
	Now          func() time.Time
}

// now is Deps.Now with its zero value resolved. A nil Now means "the caller
// did not inject a clock", which is the ordinary production case — not a
// reason to panic, and not a reason to report the zero time.Time (a
// plausible-looking January 1, year 1 that would read as a real reading).
func (d Deps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// check refuses a Deps that cannot answer honestly. Both fields are
// zero-value traps of the kind global-constraints.md names: a nil Cfg
// reaches diff.Build as a nil dereference, and an empty Cwd silently roots
// the runs tree at the PROCESS working directory — which resolves, lists
// nothing, and reports an empty review queue for a project that has runs.
// "Nothing to review" is the most reassuring wrong answer this package can
// give, so the empty value is refused rather than defaulted.
func (d Deps) check() error {
	if strings.TrimSpace(d.Cwd) == "" {
		return errors.New("serve: Deps.Cwd is empty — the queue would be read from the process working directory and an empty result would read as \"nothing to review\"")
	}
	if d.Cfg == nil {
		return errors.New("serve: Deps.Cfg is nil — the diff engine cannot run without a config (config.Discover returns one even when there is no retrace.yaml)")
	}
	return nil
}

// ScoreOf is the ONE definition of the worst-first sort key. It is exported
// because it is tested directly and because the ordering is part of the
// REST contract; it is NOT re-derived anywhere else, in Go or in
// TypeScript — the UI reads Item.score.
//
//	1000  the verdict is "failed" (or "quarantined" — see below)
//	 100  per gate
//	  10  per hop route that appeared or vanished
//	   1  per changed checkpoint
//	   1  per changed / missing / extra wire call
//	     … and then a FLOOR: any verdict other than "pass" scores above zero.
//
// A passing flow scores 0 and the UI collapses it, which is exactly why
// "quarantined" scores with "failed" rather than falling through to the
// default. A quarantined Summary has empty Counts and no Gates by
// construction (diff.Build returns before any plane is computed), so a
// formula keyed only on "failed" would give a run NOBODY COMPARED the same
// 0 as a run that was compared and matched — the zero-value trap in
// global-constraints.md, landing on the one surface whose job is to make
// sure a human looks. "Could not evaluate" sorts with the worst, never with
// the clean.
//
// THE FLOOR, and it is not a tie-break nicety. The weighted terms above are
// not the same set diff.changed() counts: Counts.WireMoved, Conformance and
// UnexpectedStatuses all make a flow "changed" and none of them appears in
// the sum. So a reorder-only flow came out Verdict:"changed", Gates:[],
// Score:0 — and score 0 is not a display detail, it is the wire contract for
// "nothing to act on". EmptyReasonFor then saw every item at zero and
// answered "all-clear", the UI rendered "none of them needs attention", and
// the changed row sat underneath that sentence inside a disclosure labelled
// "N passing". A flow that changed, reported as a clean project.
//
// Floored rather than given weights for the three missing counts, because
// inventing weights makes this formula the second place that decides what
// counts as a change; the verdict already decided, and diff.changed() is the
// one home for it. Any non-"pass" verdict — including the zero value, which
// is not "pass" — scores above zero, so `score == 0` stays an exact test for
// "this flow passed" instead of an approximation of it. That single rule
// corrects the ordering, the queue's collapse partition, the disclosure
// label and EmptyReasonFor together.
func ScoreOf(s diff.Summary) float64 {
	score := 0.0
	switch s.Verdict {
	case "failed", "quarantined":
		score += 1000
	}
	score += 100 * float64(len(s.Gates))
	score += 10 * float64(s.Counts.HopNew+s.Counts.HopGone)
	score += float64(s.Counts.PixelChanged)
	score += float64(s.Counts.WireChanged + s.Counts.WireMissing + s.Counts.WireExtra)
	if score == 0 && s.Verdict != "pass" {
		score = 1
	}
	return score
}

// diffDir is where a flow's generated diff/overlay PNGs live. It is under
// .retrace/, which is gitignored wholesale, so nothing this writes can
// reach a commit. app and flow are validated by the caller before they get
// here — see SummaryFor, which is the one construction seam.
func diffDir(cwd, app, flow string) string {
	return filepath.Join(cwd, ".retrace", "diffs", app, flow)
}

// BuildQueue diffs every recorded flow and returns them worst first.
//
// A flow that cannot be diffed at all — no reference yet, an unreadable
// manifest, a broken run directory — becomes an ITEM, never a dropped row
// and never an error that takes the whole queue down with it. One broken
// flow silently missing from a review queue is indistinguishable from a
// flow that passed, which is the failure this whole surface exists to
// prevent; the item carries verdict "failed" and a gate naming what went
// wrong, so it sorts to the top and says why.
//
// The only error returned is the one that makes the queue itself
// meaningless: the runs root cannot be read. An empty slice from a
// permission error would report "nothing to review".
func BuildQueue(d Deps) ([]Item, error) {
	if err := d.check(); err != nil {
		return nil, err
	}
	root := runs.RunsRoot(d.Cwd)
	apps, err := runs.ListAppsErr(root)
	if err != nil {
		return nil, fmt.Errorf("serve: cannot read the runs root %s: %w", root, err)
	}
	items := []Item{}
	for _, app := range apps {
		flows, ferr := runs.ListFlowsErr(root, app)
		if ferr != nil {
			items = append(items, brokenItem(app, "", ferr))
			continue
		}
		for _, flow := range flows {
			s, serr := SummaryFor(d, app, flow)
			if serr != nil {
				items = append(items, brokenItem(app, flow, serr))
				continue
			}
			items = append(items, itemOf(s))
		}
	}
	// Worst first; ties broken by app/flow so two flows that scored the
	// same appear in the same order on every reload. SliceStable keeps the
	// listing order for anything the comparison still calls equal.
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		if a.App != b.App {
			return a.App < b.App
		}
		return a.Flow < b.Flow
	})
	return items, nil
}

// The two reasons a review queue can have nothing in it for a human to act
// on. They are values on the wire, not prose, because the UI and an agent
// must reach the same conclusion from the same document.
const (
	// EmptyNoRuns: nothing has been recorded under .retrace/runs/ at all.
	// This is a setup step nobody has done, not a clean project.
	EmptyNoRuns = "no-runs"
	// EmptyAllClear: every recorded flow was compared and every one of them
	// scored zero. This is the reassuring one, and it is the one that has
	// to be earned.
	EmptyAllClear = "all-clear"
)

// EmptyReasonFor names WHY the queue has nothing to act on, or "" when it
// has something.
//
// R-O: *"'no runs have been recorded yet' and 'every run was reviewed and
// nothing needs attention' are different worlds, and an empty list renders
// them identically. The second reads as reassurance. The first is a setup
// step nobody has done."* A reviewer who reads the first as the second
// concludes the project is clean on the strength of never having recorded
// anything — which is the Zero-Value Constraint's third clause on the one
// surface whose whole job is to make a human look.
//
// The distinction is on the SCREEN, not on the slice. A fully-reviewed
// project still produces one row per flow, all at score 0, and the UI
// collapses every zero-score row (that is what the score-0 contract is
// for), so both worlds arrive at the reviewer as an empty review screen.
// Keying only on len(items) == 0 would make "all-clear" a state production
// cannot construct — a test of a hypothetical, which global-constraints.md
// forbids by name.
//
// "" is the zero value and it is the SAFE one: it promises nothing.
// EmptyAllClear is the affirmatively reassuring answer, so it is the one
// that requires positive evidence — at least one flow, compared, and every
// one of them scoring zero.
func EmptyReasonFor(items []Item) string {
	if len(items) == 0 {
		return EmptyNoRuns
	}
	for _, it := range items {
		if it.Score > 0 {
			return ""
		}
	}
	return EmptyAllClear
}

// itemOf folds one Summary into its queue line. One folding, used by both
// the queue route and the item route — a second would be a second answer
// for the same flow.
func itemOf(s diff.Summary) Item {
	return Item{
		App:      s.App,
		Flow:     s.Flow,
		Verdict:  s.Verdict,
		Score:    ScoreOf(s),
		RunID:    s.B.RunID,
		RefRunID: s.A.RunID,
		Counts:   s.Counts,
		Capture:  s.Capture,
		Gates:    itemGates(s),
	}
}

// itemGates is what the queue shows as the reasons this row needs
// attention. It is Summary.Gates plus the quarantine reasons, because a
// quarantined Summary has no Gates at all and a row that says
// "quarantined" with nothing beside it tells a reviewer to go and read the
// manifest themselves.
func itemGates(s diff.Summary) []string {
	// Non-nil even when there is nothing to say: a nil slice marshals to
	// `null`, which is the same undefined-shaped hazard as the omitted key
	// wearing a different hat. See Item.Gates.
	out := make([]string, 0, len(s.Gates)+len(s.Quarantined))
	out = append(out, s.Gates...)
	for _, q := range s.Quarantined {
		reason := q.Reason
		if reason == "" {
			reason = "no reason recorded"
		}
		out = append(out, fmt.Sprintf("side %s was quarantined: %s", q.Side, reason))
	}
	return out
}

// brokenItem is the queue line for a flow that could not be diffed. The
// verdict is "failed" — not "pass" with an empty Counts, which is what
// dropping the error would produce, and which would announce a clean flow
// on the strength of never having looked at it.
func brokenItem(app, flow string, err error) Item {
	return itemOf(diff.Summary{
		Schema: diff.SummarySchema, App: app, Flow: flow,
		Verdict: "failed",
		Gates:   []string{err.Error()},
	})
}

// SummaryFor diffs one flow's reference against its latest run — the same
// comparison `retrace diff` makes, assembled through the same
// diff.OptionsFor seam, so the queue and the CLI cannot disagree about what
// a flow's verdict is.
func SummaryFor(d Deps, app, flow string) (diff.Summary, error) {
	return summaryFor(d, app, flow, "latest")
}

// summaryFor is SummaryFor with the B-side selector exposed, for the reject
// verb (which captures a named run, not necessarily the newest one).
func summaryFor(d Deps, app, flow, selector string) (diff.Summary, error) {
	if err := d.check(); err != nil {
		return diff.Summary{}, err
	}
	// The construction seam: app and flow are request-supplied and are
	// about to be joined into diffDir below, on top of the joins runs and
	// refs each guard themselves. See global-constraints.md — the guard
	// lives where the join happens, and delegates to the one guard body.
	if err := runs.ValidateComponents(app, flow); err != nil {
		return diff.Summary{}, err
	}
	root := runs.RunsRoot(d.Cwd)

	ref := refs.Resolve(d.Cwd, root, app, flow)
	if ref.Kind == "none" {
		return diff.Summary{}, fmt.Errorf("no reference for %s/%s: %s — run `retrace ref accept --flow %s` once this flow has a good run", app, flow, ref.Reason, flow)
	}
	a := diff.RunRef{RunID: ref.RunID, Kind: ref.Kind, Dir: ref.Dir, Manifest: ref.Manifest}

	id := runs.FindRun(root, app, flow, selector)
	if id == "" {
		return diff.Summary{}, fmt.Errorf("no run matches %q for %s/%s", selector, app, flow)
	}
	// With no committed bundle, "reference" falls back to the newest
	// ELIGIBLE RUN — which, for a flow that has been recorded once, is the
	// very run under review. Diffing it against itself reports "pass": a
	// review queue announcing that nothing changed, computed by comparing a
	// run with itself, on a flow nobody has ever accepted a reference for.
	// That is the plausible-value trap in its most expensive form, so it is
	// an item with a reason instead. (`retrace ref reject` refuses the same
	// comparison for the same reason — see cmd_ref.go's rejectSummary.)
	if a.Kind == "run" && a.RunID == id {
		return diff.Summary{}, fmt.Errorf("the only reference available for %s/%s is %s itself — the run under review; run `retrace ref accept --flow %s` on a known-good run first, or this queue would be comparing a run against itself and reporting a pass", app, flow, id, flow)
	}

	p, err := runs.PathsFor(root, app, flow, id)
	if err != nil {
		return diff.Summary{}, err
	}
	m, err := runs.ReadManifest(p.ManifestPath)
	if err != nil {
		return diff.Summary{}, fmt.Errorf("reading the manifest for %s/%s/%s: %w", app, flow, id, err)
	}
	b := diff.RunRef{RunID: id, Kind: "run", Dir: p.RunDir, Manifest: m}

	opts, err := diff.OptionsFor(d.Cfg, a.Manifest, b.Manifest)
	if err != nil {
		return diff.Summary{}, err
	}
	// WantImages, into a per-flow directory under .retrace/diffs: the
	// review UI's whole job is showing the two shots and what changed
	// between them, and GET /api/shots/.../diff serves exactly the files
	// this writes. OutDir is NOT side B's run directory (which `retrace
	// diff` uses) — a review server must not write into the recording it
	// is reviewing.
	return diff.Build(diff.BuildInput{
		App: app, Flow: flow, A: a, B: b, Cfg: d.Cfg, Options: opts,
		WantImages: true, OutDir: diffDir(d.Cwd, app, flow),
	})
}
