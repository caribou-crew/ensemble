package serve

// export.go is `retrace export`: the review queue as a STATIC ARTIFACT.
//
// Everything else in this package answers a live client that can ask again.
// This one writes a directory that opens with file://, needs no server, and
// is read by someone who was not there when it was produced and cannot
// re-query anything. That is why the honesty rules this package already
// follows are applied here more strictly rather than less: a live screen can
// be refreshed, an artifact is a frozen thing a human trusts.
//
// The report is server-rendered Go html/template, not the React app. The
// React app is a live client of a REST API; making it work from file:// means
// a build variant, a fetch shim and inlined JSON — three things that can
// drift from the real UI silently. A separate, simpler, always-honest
// rendering is the smaller lie, and summary.json ships alongside so an agent
// reads the same document the UI reads.

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/retrace/diff"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

// ExportOptions is an INPUT and carries no json tags; ExportResult is
// printed by `retrace export --json`, so it is a wire type and does.
// Empty App/Flow means "everything".
type ExportOptions struct {
	Deps      Deps
	OutDir    string
	App, Flow string
}

// ExportResult is what `retrace export --json` prints.
type ExportResult struct {
	Dir   string   `json:"dir"`
	Files []string `json:"files"` // slash-separated, relative to Dir, sorted
	Items int      `json:"items"`
	// ExitCode is the CI contract for the whole export: the worst
	// diff.ExitCode across everything exported. It is a VALUE on this
	// document as well as the process' exit status, because an agent that
	// reads the artifact must be able to see the build result without
	// having watched the process that produced it.
	//
	// FOUR values, not three: 0 pass, 1 changed, 2 failed, **3
	// quarantined** — and 3 is the HIGHEST, because "nobody could evaluate
	// this" must not exit like something that was evaluated. The mapping
	// lives in diff.ExitCode and nowhere else; see exitCodeFor.
	ExitCode int `json:"exitCode"`
}

//go:embed report.tmpl.html
var reportTemplateSource string

var reportTemplate = template.Must(template.New("report").Funcs(template.FuncMap{
	"score": formatScore,
	"pct":   func(f float64) string { return strconv.FormatFloat(f, 'f', 2, 64) },
	"value": formatValue,
}).Parse(reportTemplateSource))

// formatScore renders ScoreOf's value for the data-score attribute. Integral
// scores render without a trailing ".0" so the attribute reads as the number
// a human would write; it is exported to the template only.
func formatScore(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// formatValue renders one side of a body-field diff. The values come out of
// a RECORDED response body, so they are attacker-influenced in any real
// stack: this returns a plain string and html/template escapes it in
// whatever context the template puts it. It never returns template.HTML.
func formatValue(v any) string {
	switch t := v.(type) {
	case nil:
		return "(absent)"
	case string:
		return t
	default:
		return fmt.Sprint(v)
	}
}

// exitCodeFor is the ONE seam between a verdict and the CI contract, and it
// DELEGATES: diff.ExitCode already handles all four verdicts, including
// "quarantined", which is the highest code and the one a hand-written
// three-case switch here would silently drop to 0.
//
// `retrace export` is designed to be the only step in a CI job, so the
// number this produces IS the build result. A row that could not be
// evaluated must never contribute a code that reads as a pass or a soft
// failure. Summary carries nothing else ExitCode reads, so passing a verdict
// through it is the whole call — the point is that the MAPPING is not
// re-derived here.
func exitCodeFor(verdict string) int { return diff.ExitCode(diff.Summary{Verdict: verdict}) }

// CaptureNotAssessed is the TrustReason.Code a capture banner carries when
// no capture verdict was ever computed, because the comparison that would
// have read the recordings never ran. It is the machine-readable half of
// notAssessed (queue.go), and it exists as a constant because there are now
// two places that care: the one that writes it and the one that must not
// render such a row as measured-and-clean.
const CaptureNotAssessed = "capture-not-assessed"

// unEvaluable reports whether this row is one no comparison ever produced.
//
// It reads the WIRE — the capture banner's reason code — rather than knowing
// that brokenItem exists. That was the stated reason for fixing `capture` at
// the source in Task 15: a consumer must be able to tell "assessed and found
// unusable" from "never assessed at all" without parsing prose or importing
// a private construction detail.
//
// F.15 is still open and stays open: brokenItem's runId is "" and its counts
// are twelve zeros. This is the consumer declining to RENDER them, which is
// what the reason code was for.
func unEvaluable(it Item) bool {
	return trustNotAssessed(it.Capture.A) || trustNotAssessed(it.Capture.B)
}

func trustNotAssessed(t runs.CaptureTrust) bool {
	for _, r := range t.Reasons {
		if r.Code == CaptureNotAssessed {
			return true
		}
	}
	return false
}

// --- the view ------------------------------------------------------------

// reportRow is one line of the overview.
type reportRow struct {
	Key      string // "<app>/<flow>", the row's identity in the document
	App      string
	Flow     string
	Href     string // relative link to this flow's own page
	Verdict  string
	Score    float64
	Gates    []string
	Capture  diff.CaptureBanner
	Reasons  []runs.TrustReason
	RunID    string
	RefRunID string
	// Compared is false for every row where NO PLANE WAS COMPUTED: a flow
	// that could not be diffed at all (unEvaluable) and a flow whose
	// comparison was refused because a side's capture was not trusted
	// (quarantined). Both have an all-zero Counts, and twelve zeros render
	// as "measured, and fine" — the exact reading this artifact has the
	// least context available to correct. When it is false the row prints
	// NEITHER a counts strip NOR a run id it does not have, and prints
	// WhyNot instead.
	Compared bool
	WhyNot   string
	Counts   string // the strip; "" when there is nothing to say
	Compare  string // what was actually compared, e.g. "3 checkpoints · 8 wire calls"
}

type reportIndex struct {
	Version string
	Empty   string
	Rows    []reportRow
	Total   int
	Failing int
}

type reportItem struct {
	Row     reportRow
	Summary *diff.Summary // nil when nothing was compared
	Shots   []reportShot
}

type reportShot struct {
	Name    string
	Verdict string
	Cp      diff.CheckpointVerdict
	Sides   []reportShotSide
	Missing []string // sides the summary named that are not in this export
}

type reportShotSide struct {
	Side  string
	Label string
	Src   string
}

// --- Export --------------------------------------------------------------

// Export writes the static report.
//
// The ordering is the QUEUE'S: BuildQueue diffs every flow and returns them
// worst-first by ScoreOf, and this renders that order. It does not sort on
// verdict, on counts or on a fresh weighting — a second ordering here would
// be a second answer to "which of these needs attention most" in the one
// document a reader has when they have no other source.
func Export(o ExportOptions) (ExportResult, error) {
	if err := o.Deps.check(); err != nil {
		return ExportResult{}, err
	}
	if strings.TrimSpace(o.OutDir) == "" {
		return ExportResult{}, errors.New("serve: Export needs an output directory — writing a CI artifact into the process working directory by default is how a report ends up somewhere nobody uploads")
	}
	// app and flow arrive from CLI flags and are joined into OutDir below.
	// The guard lives at the construction seam and DELEGATES to the one
	// guard body — runs.ValidateComponents, whose own doc names the cost of
	// a second call site without it.
	for _, c := range []string{o.App, o.Flow} {
		if c == "" {
			continue
		}
		if err := runs.ValidateComponents(c); err != nil {
			return ExportResult{}, err
		}
	}

	all, err := BuildQueue(o.Deps)
	if err != nil {
		return ExportResult{}, err
	}
	items := filterQueue(all, o.App, o.Flow)
	// A filter that matched nothing is an ERROR naming what it looked for.
	// Writing an empty report for a typo'd --flow would be a green CI job
	// over nothing at all. An unfiltered export of a project with no runs
	// is a different thing entirely, and it renders as the setup step it is
	// (see EmptyReasonFor).
	if len(items) == 0 && (o.App != "" || o.Flow != "") {
		return ExportResult{}, fmt.Errorf("no recorded flow matches %s: nothing under %s matched, so there is nothing to export — check the name, or run `retrace run` first", filterDesc(o.App, o.Flow), runs.RunsRoot(o.Deps.Cwd))
	}

	e := &exporter{opts: o, root: o.OutDir}
	rows := make([]reportRow, 0, len(items))
	worst := 0
	for _, it := range items {
		row, err := e.item(it)
		if err != nil {
			return ExportResult{}, err
		}
		if c := exitCodeFor(row.Verdict); c > worst {
			worst = c
		}
		rows = append(rows, row)
	}

	failing := 0
	for _, r := range rows {
		if r.Score > 0 {
			failing++
		}
	}
	// EmptyReasonFor is the ONE place that decides which of the two empty
	// worlds this is: "no-runs" is a setup step nobody has done and
	// "all-clear" is the reassuring one, which has to be earned. Re-deriving
	// it here from len(rows) would hand out the reassuring answer for free.
	idx := reportIndex{
		Version: o.Deps.Version,
		Empty:   EmptyReasonFor(items),
		Rows:    rows,
		Total:   len(rows),
		Failing: failing,
	}
	if err := e.render("index.html", "index", idx); err != nil {
		return ExportResult{}, err
	}

	sort.Strings(e.files)
	return ExportResult{Dir: o.OutDir, Files: e.files, Items: len(items), ExitCode: worst}, nil
}

func filterDesc(app, flow string) string {
	switch {
	case app != "" && flow != "":
		return fmt.Sprintf("app %q flow %q", app, flow)
	case flow != "":
		return fmt.Sprintf("flow %q", flow)
	default:
		return fmt.Sprintf("app %q", app)
	}
}

// filterQueue narrows the queue without reordering it.
func filterQueue(items []Item, app, flow string) []Item {
	out := make([]Item, 0, len(items))
	for _, it := range items {
		if app != "" && it.App != app {
			continue
		}
		if flow != "" && it.Flow != flow {
			continue
		}
		out = append(out, it)
	}
	return out
}

// --- one flow ------------------------------------------------------------

type exporter struct {
	opts  ExportOptions
	root  string
	files []string
}

// item writes one flow's directory and returns its overview row.
//
// The layout NESTS: <out>/<app>/<flow>/. It does not join the two into one
// component. `runs.validateComponent` permits underscores, so an
// `<app>__<flow>` join collides — web__search/x and web/search__x produce
// the same directory, the second export overwrites the first's report and
// its shots MERGE into the first's tree with the PNG names deciding which
// wins. Nothing errors, nothing logs, the HTML renders perfectly, and
// snake_case flow names are ordinary. The filesystem already has a separator
// that cannot collide, and the runs root already uses it.
func (e *exporter) item(it Item) (reportRow, error) {
	row := reportRow{
		Key: it.App + "/" + it.Flow, App: it.App, Flow: it.Flow,
		Verdict: it.Verdict, Score: it.Score, Gates: it.Gates,
		Capture: it.Capture,
		Reasons: append(append([]runs.TrustReason{}, it.Capture.A.Reasons...), it.Capture.B.Reasons...),
	}

	// The join guard, at the join, delegated to the one guard body:
	// it.App/it.Flow are about to become a directory under OutDir.
	//
	// A row that cannot pass it is NOT dropped and does NOT take the export
	// down. BuildQueue emits brokenItem(app, "", err) for an app whose flow
	// listing failed — a real, reachable state (an unreadable app
	// directory), and a row that names no flow at all. It still belongs in
	// the report, because a broken flow silently missing from a review
	// queue is indistinguishable from a flow that passed; what it does not
	// get is a directory of its own or a link to a page that does not
	// exist.
	if err := runs.ValidateComponents(it.App, it.Flow); err != nil {
		row.WhyNot = fmt.Sprintf("could not be evaluated — this row names no flow that can be written to a directory (%v), so it appears here and has no page of its own", err)
		return row, nil
	}
	dir := path.Join(it.App, it.Flow)
	row.Href = path.Join(dir, "index.html")

	if unEvaluable(it) {
		row.WhyNot = "could not be evaluated — this flow was never compared, so nothing below is a finding about it"
		return row, e.render(path.Join(dir, "index.html"), "item", reportItem{Row: row})
	}

	sum, err := SummaryFor(e.opts.Deps, it.App, it.Flow)
	if err != nil {
		// BuildQueue compared this flow and a second read disagreed. The
		// row becomes the SAME un-evaluable row brokenItem builds — one
		// construction for "a flow that could not be compared" — rather
		// than keeping a verdict computed from a comparison this export
		// could not reproduce.
		broken := brokenItem(it.App, it.Flow, err)
		row.Verdict, row.Score, row.Gates = broken.Verdict, broken.Score, broken.Gates
		row.Capture = broken.Capture
		row.Reasons = append(append([]runs.TrustReason{}, broken.Capture.A.Reasons...), broken.Capture.B.Reasons...)
		row.WhyNot = "could not be evaluated — this flow was never compared, so nothing below is a finding about it"
		return row, e.render(path.Join(dir, "index.html"), "item", reportItem{Row: row})
	}

	// A quarantined Summary has empty Counts and no Checkpoints by
	// construction: diff.Build returns before any plane is computed. It is
	// a DIFFERENT state from un-evaluable and says so in its own words, but
	// it has the same consequence for the strip — nothing was compared, so
	// nothing here may be reported as clean.
	if sum.Verdict == "quarantined" {
		row.WhyNot = "could not be evaluated — the comparison was refused because a side's capture was not trusted"
		return row, e.render(path.Join(dir, "index.html"), "item", reportItem{Row: row, Summary: &sum})
	}

	row.Compared = true
	row.RunID, row.RefRunID = it.RunID, it.RefRunID
	row.Counts = countsStrip(it.Counts)
	row.Compare = comparedStrip(sum)

	if err := e.writeJSON(path.Join(dir, "summary.json"), sum); err != nil {
		return row, err
	}
	shots, err := e.shots(dir, sum)
	if err != nil {
		return row, err
	}
	return row, e.render(path.Join(dir, "index.html"), "item", reportItem{Row: row, Summary: &sum, Shots: shots})
}

// shots copies every checkpoint image the comparison produced into the
// export and returns what the page may reference.
//
// The four sides keep their `<side>/shots/<name>.png` shape (Task 10's
// layout contract) rather than being re-laid-out on copy, so summary.json's
// own `images` paths stay valid INSIDE the export and an agent can join them
// without a second rule. A and B are COPIED: a CI artifact has no access to
// the run directories they normally resolve against.
//
// A side the summary named but whose file is not there is reported as
// MISSING rather than referenced. A broken <img> renders as a blank pane,
// and a blank comparison pane reads as "identical".
func (e *exporter) shots(dir string, sum diff.Summary) ([]reportShot, error) {
	out := make([]reportShot, 0, len(sum.Checkpoints))
	for _, cp := range sum.Checkpoints {
		shot := reportShot{Name: cp.Name, Verdict: cp.Verdict, Cp: cp}
		for _, side := range shotSides {
			switch side {
			case "a":
				if cp.Images.A == "" {
					continue
				}
			case "b":
				if cp.Images.B == "" {
					continue
				}
			case "diff":
				if cp.Images.Diff == "" {
					continue
				}
			case "overlay":
				if cp.Images.Overlay == "" {
					continue
				}
			}
			// The same two functions the review server resolves a shot
			// with, so the export and the API cannot disagree about where
			// a checkpoint's PNG lives — safeShotPath is also the guard on
			// the checkpoint name, which comes off a manifest.
			srcDir, err := shotDirFor(&sum, diffDir(e.opts.Deps.Cwd, sum.App, sum.Flow), side)
			if err != nil {
				return nil, err
			}
			src, err := safeShotPath(srcDir, cp.Name)
			if err != nil {
				return nil, err
			}
			rel := path.Join(side, "shots", path.Base(filepath.ToSlash(src)))
			copied, err := e.copy(path.Join(dir, rel), src)
			if err != nil {
				return nil, err
			}
			if !copied {
				shot.Missing = append(shot.Missing, side)
				continue
			}
			shot.Sides = append(shot.Sides, reportShotSide{Side: side, Label: sideLabel(side), Src: rel})
		}
		out = append(out, shot)
	}
	return out, nil
}

func sideLabel(side string) string {
	switch side {
	case "a":
		return "reference"
	case "b":
		return "this run"
	case "diff":
		return "changed pixels"
	default:
		return "overlay"
	}
}

// countsStrip is the one-line tally on a row that WAS compared. Only planes
// with something to say appear: "0 shots · 0 wire" on every row is noise
// that trains a reader to skip the strip.
//
// It covers every count diff.changed() keys on. That is not tidiness: when
// wireMoved, conformance and unexpectedStatuses were missing from the live
// UI's strip, a reorder-only flow rendered an amber "changed" badge with an
// EMPTY strip, and the reader's only move was to open the flow to find out
// whether anything was wrong at all. In an artifact there is no "open the
// flow".
func countsStrip(c diff.Counts) string {
	var parts []string
	add := func(n int, label string) {
		if n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, label))
		}
	}
	add(c.PixelChanged, "shots")
	add(c.WireChanged+c.WireMissing+c.WireExtra, "wire")
	add(c.WireMoved, "reordered")
	add(c.HopNew, "new hop routes")
	add(c.HopGone, "gone hop routes")
	add(c.Violations, "violations")
	add(c.UnexpectedStatuses, "unexpected statuses")
	add(c.Conformance, "conformance")
	return strings.Join(parts, " · ")
}

// comparedStrip says what was actually LOOKED AT, which is the other half of
// an empty countsStrip: "no differences" over three checkpoints and eight
// wire calls is a result, and "no differences" over nothing is not.
func comparedStrip(s diff.Summary) string {
	return fmt.Sprintf("%d checkpoints · %d wire calls compared", s.Counts.Checkpoints, s.Counts.WirePaired+s.Counts.WireMissing+s.Counts.WireExtra)
}

// --- writing -------------------------------------------------------------

func (e *exporter) abs(rel string) string { return filepath.Join(e.root, filepath.FromSlash(rel)) }

func (e *exporter) record(rel string) { e.files = append(e.files, rel) }

func (e *exporter) create(rel string) (*os.File, error) {
	dst := e.abs(rel)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return nil, fmt.Errorf("serve: export: %w", err)
	}
	f, err := os.Create(dst)
	if err != nil {
		return nil, fmt.Errorf("serve: export: %w", err)
	}
	return f, nil
}

func (e *exporter) render(rel, name string, data any) error {
	f, err := e.create(rel)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := reportTemplate.ExecuteTemplate(f, name, data); err != nil {
		return fmt.Errorf("serve: rendering %s: %w", rel, err)
	}
	e.record(rel)
	return nil
}

func (e *exporter) writeJSON(rel string, v any) error {
	f, err := e.create(rel)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := writeJSONTo(f, v); err != nil {
		return fmt.Errorf("serve: writing %s: %w", rel, err)
	}
	e.record(rel)
	return nil
}

// copy reports false, with no error, when the source is not there: a shot
// the summary named and the run directory does not hold is a fact to
// REPORT, not an error that takes the whole export down.
func (e *exporter) copy(rel, src string) (bool, error) {
	in, err := os.Open(src)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("serve: export: reading %s: %w", src, err)
	}
	defer in.Close()
	out, err := e.create(rel)
	if err != nil {
		return false, err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return false, fmt.Errorf("serve: export: writing %s: %w", rel, err)
	}
	e.record(rel)
	return true, nil
}

// writeJSONTo is the export's JSON writer. It is not server.writeJSON:
// that one writes a status line and a Content-Type to an
// http.ResponseWriter, and an artifact is a file.
func writeJSONTo(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// CaptureSides is the template's test for a capture side worth bannering —
// the same test every other consumer in this package makes (Status !=
// VerdictOK, never Status == VerdictBroken; capture.Assess's own doc rules
// that out).
func (r reportRow) CaptureSides() []reportCaptureSide {
	var out []reportCaptureSide
	for _, s := range []struct {
		side  string
		trust runs.CaptureTrust
	}{{"a", r.Capture.A}, {"b", r.Capture.B}} {
		if s.trust.Status == trace.VerdictOK {
			continue
		}
		label := "reference"
		if s.side == "b" {
			label = "this run"
		}
		status := string(s.trust.Status)
		if status == "" {
			// The zero value is not a verdict anybody reached; it is the
			// absence of one, and it must not render as a blank badge that
			// reads as a rendering glitch. No production path sends it any
			// more (Task 15's N-3 fixed brokenItem at the source), but
			// trace.Verdict is a Go string type whose zero value is still
			// "", so the next construction path that forgets the field is
			// caught here rather than shown as nothing at all.
			status = "not assessed"
		}
		out = append(out, reportCaptureSide{Side: s.side, Label: label, Status: status, Trust: s.trust})
	}
	return out
}

type reportCaptureSide struct {
	Side   string
	Label  string
	Status string
	Trust  runs.CaptureTrust
}
