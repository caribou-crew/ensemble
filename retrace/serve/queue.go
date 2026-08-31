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

	"github.com/caribou-crew/ensemble/core/trace"
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
	// Source is nil for a run recorded locally — see runs.Source's own doc
	// comment for why nil, not a "local" value, is the encoding — and set
	// for one `retrace sync` merged in. omitempty because a queue is
	// overwhelmingly local runs today, and `"source":null` on every row
	// would train a reader to ignore the key on the rows where it matters.
	Source *runs.Source `json:"source,omitempty"`
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
	// CfgFor resolves the config governing ONE app, for a dashboard that
	// diffs runs recorded against more than one retrace.yaml (synced from
	// different repositories, or from a monorepo client in its own
	// subdirectory — see ensemble/config.RetraceConfig.Apps, which is what
	// builds this closure for ensemble/server). nil — retrace serve's own
	// single-project CLI use, and every existing caller of this package —
	// means "every app uses Cfg", unchanged from before this field
	// existed. See configFor.
	CfgFor func(app string) (*config.Config, error)
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

// configFor resolves the config that governs one app: d.CfgFor(app) when
// set, else d.Cfg. See CfgFor's own doc comment for why nil means "every
// app uses Cfg".
func (d Deps) configFor(app string) (*config.Config, error) {
	if d.CfgFor == nil {
		return d.Cfg, nil
	}
	return d.CfgFor(app)
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

// diffDirForRun is diffDir's counterpart for a comparison pinned to a
// specific B-side run rather than "latest". Without the runID component, a
// non-latest run's generated diff/overlay PNGs would land at the exact same
// path the "latest" queue itself reads and writes — diff.writeCheckpointImages
// keys a PNG by checkpoint NAME alone, so two different runs sharing a
// checkpoint name would silently clobber each other's cached image, and a
// concurrent request for "latest" and for an older run could hand either
// caller the wrong run's picture. runID is validated by the caller before
// it gets here — see SummaryForRun, which is the one construction seam.
func diffDirForRun(cwd, app, flow, runID string) string {
	return filepath.Join(cwd, ".retrace", "diffs", app, flow, "runs", runID)
}

// BuildQueue diffs every recorded flow and returns them worst first.
//
// A flow that cannot be diffed at all — no reference yet, an unreadable
// manifest, a broken run directory — becomes an ITEM, never a dropped row
// and never an error that takes the whole queue down with it. One broken
// flow silently missing from a review queue is indistinguishable from a
// flow that passed, which is the failure this whole surface exists to
// prevent; the item carries verdict "quarantined" — a comparison that could
// not be made — and a gate naming what went wrong, so it sorts to the top
// and says why.
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
		Source:   sourceOf(s.B.Dir),
	}
}

// sourceOf reads the run-under-review's provenance sidecar, best-effort: a
// missing or unreadable source.json degrades to nil (indistinguishable from
// "recorded locally"), the same way a run captured before an adapter wrote
// device.json degrades to Device being nil rather than failing the whole
// item. Provenance is a display convenience, never an input to a verdict —
// see runs.Source's own doc comment — so nothing here can change Verdict,
// Score or Gates.
func sourceOf(runDir string) *runs.Source {
	if runDir == "" {
		return nil
	}
	src, err := runs.ReadSource(runs.Paths{RunDir: runDir})
	if err != nil {
		return nil
	}
	return src
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

// notAssessed is the capture trust of a flow whose comparison never
// happened, and it exists so that the ZERO VALUE is not what crosses the
// wire.
//
// brokenItem used to fold a hand-populated diff.Summary{} into a row, which
// left Item.Capture a zero diff.CaptureBanner: `{"a":{"status":"","summary":""},
// "b":{…}}`. That is a capture-trust VALUE asserted for a capture nobody
// examined, on the one type whose entire purpose is to say how much the
// evidence below it can be trusted — global-constraints.md's zero-value rule
// on exactly the field it was written for. Every consumer then has to know
// that brokenItem exists in order to read the row correctly, and the second
// consumer (Task 16's static CI report) would inherit it with nothing to
// protect it.
//
// trace.VerdictFailed, not a new member of the dialect: it is documented "no
// usable capture", it is the worst rank, and "no usable capture" is exactly
// this row's situation. The MACHINE-READABLE distinction between "assessed
// and found unusable" and "never assessed at all" is the reason code —
// TrustReason.Code is a plain string, so `capture-not-assessed` costs no wire
// type — and the Summary says it in a sentence for the human half.
//
// Never keyed on Status == VerdictBroken anywhere: capture.Assess's own doc
// rules that out, and every consumer here tests Status != VerdictOK.
func notAssessed(reason string) runs.CaptureTrust {
	return runs.CaptureTrust{
		Status:  trace.VerdictFailed,
		Summary: "capture not assessed — this flow could not be compared at all, so neither side's recording was ever examined",
		Hint:    "this is not a verdict about the capture; it is the absence of one. Fix the reason above and the row will carry a real capture verdict.",
		Reasons: []runs.TrustReason{{
			Code:   CaptureNotAssessed,
			Status: trace.VerdictFailed,
			Detail: reason,
			Hint:   "no capture trust was computed for either side, because the comparison that would have read them never ran",
		}},
	}
}

// brokenItem is the queue line for a flow that could not be diffed.
//
// The verdict is "quarantined", because that is what this row IS: a
// comparison that could not be made. It is emphatically NOT "pass" with an
// empty Counts — which is what dropping the error would produce, and which
// would announce a clean flow on the strength of never having looked at it —
// and the capture says it was never assessed rather than carrying the zero
// value (see notAssessed).
//
// It was "failed" until Task 16's fix round 1. That reasoning was an argument
// against "pass" and never weighed "failed" against "quarantined"; the cost
// showed up when the second consumer arrived. `retrace diff` on a flow with
// no reference exits 3 (could not evaluate) and `retrace export` exited 2
// (hard gate failed) for the SAME fact, because diff.ExitCode maps "failed"
// to 2 — two faces of one report returning different CI codes, which is the
// divergence ruleRequest's own doc comment rules against by name.
//
// Everything downstream already handles it, and none of it needed changing:
// diff.ExitCode gives 3, ScoreOf scores "quarantined" with "failed" at 1000
// so the row still sorts to the top of the queue where an un-evaluable row
// belongs, and Task 15's verdictTone is total over the verdict vocabulary
// with a "quarantined" arm.
//
// Summary.Quarantined stays EMPTY on purpose. That field names the sides a
// comparison refused and why; here neither side was examined at all, so
// naming one would assert an examination that never happened. The reason
// travels as a gate, and the machine-readable half is the capture banner's
// capture-not-assessed code.
func brokenItem(app, flow string, err error) Item {
	trust := notAssessed(err.Error())
	return itemOf(diff.Summary{
		Schema: diff.SummarySchema, App: app, Flow: flow,
		Verdict: "quarantined",
		Gates:   []string{err.Error()},
		Capture: diff.CaptureBanner{A: trust, B: trust},
	})
}

// SummaryFor diffs one flow's reference against its latest run — the same
// comparison `retrace diff` makes, assembled through the same
// diff.OptionsFor seam, so the queue and the CLI cannot disagree about what
// a flow's verdict is.
func SummaryFor(d Deps, app, flow string) (diff.Summary, error) {
	return summaryFor(d, app, flow, "latest", diffDir(d.Cwd, app, flow))
}

// SummaryForRun is SummaryFor pinned to one specific run rather than
// "latest" — the run-detail view's counterpart to SummaryFor, so a reviewer
// can inspect any run in the CI candidate list, not only the newest. Its
// generated diff/overlay images are cached under diffDirForRun rather than
// diffDir, so viewing a non-latest run never clobbers (or is clobbered by)
// the "latest" queue's own cached images for the same flow.
func SummaryForRun(d Deps, app, flow, runID string) (diff.Summary, error) {
	return summaryFor(d, app, flow, runID, diffDirForRun(d.Cwd, app, flow, runID))
}

// summaryFor is SummaryFor with the B-side selector and image-cache
// directory exposed. The reject verb is the other caller (it captures a
// named run, not necessarily the newest one); it now uses diffDirForRun via
// its own call site, the same isolation SummaryForRun gets.
func summaryFor(d Deps, app, flow, selector, outDir string) (diff.Summary, error) {
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
	cfg, err := d.configFor(app)
	if err != nil {
		return diff.Summary{}, fmt.Errorf("resolving the retrace config for app %q: %w", app, err)
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

	opts, err := diff.OptionsFor(cfg, a.Manifest, b.Manifest)
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
		App: app, Flow: flow, A: a, B: b, Cfg: cfg, Options: opts,
		WantImages: true, OutDir: outDir,
	})
}
