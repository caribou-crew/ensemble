// summary.go is the composition point for this whole phase: it takes the
// three independent diff planes built by wire.go, hop.go and openapi.go
// (plus pixel/) and folds them into the one Summary document every consumer
// reads — the CLI's text report, `--json`, the review queue, the static
// export, and any agent. There is no second aggregation path; if a surface
// shows something, it came from Build.
package diff

import (
	"fmt"
	"image"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/retrace/capture"
	"github.com/caribou-crew/ensemble/retrace/config"
	"github.com/caribou-crew/ensemble/retrace/diff/pixel"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

// SummarySchema versions the Summary document, independently of
// runs.Schema (the manifest) and trace.SchemaVersion (the hop record) —
// this document gains fields on its own schedule.
const SummarySchema = "retrace-diff/1"

// RunRef identifies one side of a diff. Kind uses ONE vocabulary across
// this package and refs (Task 11): "bundle" (an accepted reference
// bundle), "run" (a run directory), "none" (no side resolved) —
// refs.Resolve returns the same three strings.
type RunRef struct {
	RunID    string        `json:"runId"`
	Kind     string        `json:"kind"` // "bundle" | "run" | "none"
	Dir      string        `json:"dir"`
	Manifest runs.Manifest `json:"manifest"`
}

// CheckpointVerdict is one checkpoint's pixel comparison, or the verdict
// that stands in for one when it could not be compared at all.
type CheckpointVerdict struct {
	Name        string  `json:"name"`
	Verdict     string  `json:"verdict"` // "ok" | "changed" | "missing" | "added" | "unreadable"
	DiffPct     float64 `json:"diffPct"`
	DiffPctFine float64 `json:"diffPctFine"`
	NumDiff     int     `json:"numDiff"`
	// Mismatch reports that the two SHOTS differed in size — copied from
	// pixel.Result.Mismatch, which Task 7's review pinned to the real
	// pre-trim geometry. Overlap is non-nil whenever the COMPARED images
	// differed in size, which independently trimmed same-size shots also
	// trigger. So overlap != nil does NOT imply mismatch == true, and code
	// must not treat them as one signal. When they disagree, DiffPct is
	// inflated by padding and overlap.paddingPct is how much; overlap.diffPct
	// is the honest content number.
	Mismatch bool           `json:"mismatch,omitempty"`
	Overlap  *pixel.Overlap `json:"overlap,omitempty"`
	// Trimmed reports the rects Compare actually used when the checkpoint
	// asked for border trimming, in the originals' coordinates. nil = no
	// trim requested, or trim refused.
	Trimmed *TrimRects       `json:"trimmed,omitempty"`
	Images  CheckpointImages `json:"images"` // "" for any side not written
}

type TrimRects struct {
	A *pixel.Rect `json:"a,omitempty"`
	B *pixel.Rect `json:"b,omitempty"`
}

// CheckpointImages carries the four sides of a checkpoint comparison. A and
// B are relative to Summary.A.Dir / Summary.B.Dir and are the capture's own
// path ("shots/receipt.png"); Diff and Overlay are relative to
// BuildInput.OutDir and are written by Build ("diff/shots/receipt.png").
// Every one of the four resolves as <dir>/shots/<name>.png.
type CheckpointImages struct {
	A       string `json:"a,omitempty"`
	B       string `json:"b,omitempty"`
	Diff    string `json:"diff,omitempty"`
	Overlay string `json:"overlay,omitempty"`
}

// Counts is a flat, agent-legible tally over every plane, so a caller can
// judge "how much changed" from one document without walking every field.
type Counts struct {
	Checkpoints  int `json:"checkpoints"`
	PixelChanged int `json:"pixelChanged"`
	WirePaired   int `json:"wirePaired"`
	WireChanged  int `json:"wireChanged"`
	WireMoved    int `json:"wireMoved"`
	WireMissing  int `json:"wireMissing"`
	WireExtra    int `json:"wireExtra"`
	// Violations counts rule violations from BOTH planes: every
	// Entry.BodyViolations element AND every Entry.HeaderDiff element whose
	// Type == "violation". Task 8's review found headers flattening
	// violations into "changed", which made the exit-2 gate inexpressible
	// for headers; counting only BodyViolations here reintroduces that
	// defect at the consumer. Tolerated entries on either plane are NOT
	// violations.
	Violations         int `json:"violations"`
	HopNew             int `json:"hopNew"`
	HopGone            int `json:"hopGone"`
	UnexpectedStatuses int `json:"unexpectedStatuses"`
	Conformance        int `json:"conformance"`
}

// Summary is the one document every consumer reads.
type Summary struct {
	Schema             string               `json:"schema"`
	App                string               `json:"app"`
	Flow               string               `json:"flow"`
	A                  RunRef               `json:"a"`
	B                  RunRef               `json:"b"`
	Verdict            string               `json:"verdict"` // "pass" | "changed" | "failed" | "quarantined"
	Checkpoints        []CheckpointVerdict  `json:"checkpoints"`
	Wire               Wire                 `json:"wire"`
	Sections           []Section            `json:"sections"`
	Hops               HopDiff              `json:"hops"`
	UnexpectedStatuses []StatusFinding      `json:"unexpectedStatuses"`
	Perf               PerfResult           `json:"perf"`
	Conformance        []ConformanceFinding `json:"conformance"`
	Capture            CaptureBanner        `json:"capture"`
	Counts             Counts               `json:"counts"`
	Gates              []string             `json:"gates"` // human-readable reasons the verdict is "failed"
	// Budgets is the configurable CI-gate wire contract: one entry per
	// PLANE that retrace.yaml's `gates:` key names, never one per plane
	// that merely exists. A plane `gates:` does not mention gets no entry
	// at all — "not gated" and "gated at a threshold of zero" are
	// different configurations, and a Go zero value cannot carry both
	// meanings, so the zero-value Gate is simply never constructed.
	//
	// Pixel is the exception, and NOT one this task implements: Task 3's
	// applyDefaults fills gates.pixel from thresholds.gate when the key is
	// absent, so by the time Build sees a Config the pixel plane is never
	// missing.
	//
	// fail_on (also consumed here, not defined here) says which of these
	// plane names can turn Verdict to "failed"; a plane can be measured and
	// reported without being allowed to fail the build. Named Budgets, not
	// Gates, because Gates []string above already answers to that json
	// key.
	Budgets []Gate `json:"budgets"`
	// Quarantined lists the sides excluded from this comparison because
	// their own capture-trust verdict was not "ok". Empty unless
	// --allow-degraded was NOT passed and at least one side warranted it.
	Quarantined []Quarantine `json:"quarantined,omitempty"`
}

// Gate is one configured CI budget for one diff plane, read from
// retrace.yaml's `gates:` map (e.g. `gates: {pixel: {budget_pct: 2}}`).
type Gate struct {
	Plane     string  `json:"plane"` // "pixel" | "wire" | "hop" | "perf"
	Threshold float64 `json:"threshold"`
	Observed  float64 `json:"observed"`
	Failed    bool    `json:"failed"`
}

// Quarantine records why one side of a comparison was refused instead of
// diffed. Task 6 owns the verdict this keys on; Build is where "not ok"
// becomes a refusal instead of a diff result.
type Quarantine struct {
	Side   string `json:"side"`   // "a" | "b"
	Reason string `json:"reason"` // the runs.CaptureTrust.Summary that triggered it
}

type CaptureBanner struct {
	A runs.CaptureTrust `json:"a"`
	B runs.CaptureTrust `json:"b"`
}

// BuildInput is everything Build needs to produce a Summary.
type BuildInput struct {
	App, Flow  string
	A, B       RunRef
	Cfg        *config.Config
	Options    Options
	WantImages bool
	OutDir     string // where diff/overlay PNGs are written (usually B's run dir)
	// AllowDegraded disables the default quarantine of a non-ok side
	// (--allow-degraded). false is the safe default: a run that never sets
	// it still refuses to diff a broken capture.
	AllowDegraded bool
}

// OptionsFor assembles the diff Options from the config and the two
// manifests. EVERY caller of Build uses it — cmd_diff, serve and export —
// so there is exactly one place where "what the engine was told" is
// decided.
func OptionsFor(cfg *config.Config, a, b runs.Manifest) (Options, error) {
	rs, err := cfg.Rules()
	if err != nil {
		return Options{}, err
	}
	o := Options{
		WireIgnore: cfg.WireIgnorePaths(),
		Rules:      rs,
		Normalize:  cfg.NormalizePath,
		GroupsA:    a.Groups,
		GroupsB:    b.Groups,
	}
	// TODO(task-11): load the deviations ledger here —
	//   if cfg.Deviations != "" {
	//       ds, err := LoadDeviations(filepath.Join(cfg.Dir, cfg.Deviations))
	//       o.Deviations = ResolveDeviations(ds, a.App, b.App)
	//   }
	// o.Deviations stays nil until then, which is a no-op.
	return o, nil
}

// quarantineCheck reports which sides of a and b are not trustworthy enough
// to compare at all: their own capture-trust verdict is anything but "ok" —
// deliberately WIDER than capture.Fatal, which excludes "suspect". Diffing
// against a side nobody confirmed clean is not evidence of anything.
func quarantineCheck(a, b RunRef) []Quarantine {
	var out []Quarantine
	if st := a.Manifest.Capture.Status; st != trace.VerdictOK {
		out = append(out, Quarantine{Side: "a", Reason: a.Manifest.Capture.Summary})
	}
	if st := b.Manifest.Capture.Status; st != trace.VerdictOK {
		out = append(out, Quarantine{Side: "b", Reason: b.Manifest.Capture.Summary})
	}
	return out
}

// checkpointUnion returns every checkpoint name across both manifests, in
// first-seen order (a's names, then any b-only name) — the same shape
// unionNames already gives BuildSections' group names.
func checkpointUnion(a, b runs.Manifest) []string {
	names := func(m runs.Manifest) []string {
		out := make([]string, len(m.Checkpoints))
		for i, cp := range m.Checkpoints {
			out[i] = cp.Name
		}
		return out
	}
	return unionNames(names(a), names(b))
}

func findCheckpoint(m runs.Manifest, name string) (runs.Checkpoint, bool) {
	for _, cp := range m.Checkpoints {
		if cp.Name == name {
			return cp, true
		}
	}
	return runs.Checkpoint{}, false
}

// writeCheckpointImages writes Diff/Overlay under OutDir per the layout
// contract (three tasks read these paths and only this one writes them):
//
//	diff/shots/<name>.png      overlay/shots/<name>.png
//
// The `shots/` level is the SECOND component, not the first — Task 13's
// safeShotPath is filepath.Join(dir, "shots", base+".png") and rejects any
// name containing a separator, so every side it serves must be a directory
// with a shots/ child, exactly what a run directory already is.
//
// A and B are NOT written here — the A- and B-side PNGs already exist in
// their own run directories, so A/B carry that run's own run-dir-relative
// path ("shots/receipt.png", the same string as runs.Checkpoint.File) and
// are resolved against Summary.A.Dir / Summary.B.Dir.
func writeCheckpointImages(outDir, name string, cpA, cpB runs.Checkpoint, imgs pixel.Images) (CheckpointImages, error) {
	out := CheckpointImages{A: cpA.File, B: cpB.File}
	if imgs.Diff != nil {
		rel := filepath.Join("diff", "shots", name+".png")
		if err := writePNG(filepath.Join(outDir, rel), imgs.Diff); err != nil {
			return out, err
		}
		out.Diff = filepath.ToSlash(rel)
	}
	if imgs.Overlay != nil {
		rel := filepath.Join("overlay", "shots", name+".png")
		if err := writePNG(filepath.Join(outDir, rel), imgs.Overlay); err != nil {
			return out, err
		}
		out.Overlay = filepath.ToSlash(rel)
	}
	return out, nil
}

func writePNG(path string, img *image.RGBA) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := pixel.Encode(img)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// Build produces the one document every consumer reads: the CLI's text
// report, --json, the review queue, the static export, and any agent.
func Build(in BuildInput) (Summary, error) {
	s := Summary{Schema: SummarySchema, App: in.App, Flow: in.Flow, A: in.A, B: in.B}
	s.Capture = CaptureBanner{A: in.A.Manifest.Capture, B: in.B.Manifest.Capture}

	// --- quarantine, checked before anything else is compared. A capture
	// whose own trust verdict is not "ok" makes every downstream comparison
	// confident nonsense, so this returns immediately rather than computing
	// a partial Summary. --allow-degraded is the only way past it.
	if !in.AllowDegraded {
		if q := quarantineCheck(in.A, in.B); len(q) > 0 {
			s.Quarantined = q
			s.Verdict = "quarantined"
			return s, nil
		}
	}

	// --- pixel, per checkpoint, by name union so a checkpoint that
	// appeared or vanished is its own verdict rather than a silent skip.
	for _, name := range checkpointUnion(in.A.Manifest, in.B.Manifest) {
		cpA, okA := findCheckpoint(in.A.Manifest, name)
		cpB, okB := findCheckpoint(in.B.Manifest, name)
		switch {
		case !okA:
			s.Checkpoints = append(s.Checkpoints, CheckpointVerdict{Name: name, Verdict: "added"})
			continue
		case !okB:
			s.Checkpoints = append(s.Checkpoints, CheckpointVerdict{Name: name, Verdict: "missing"})
			continue
		}
		aPNG, errA := os.ReadFile(filepath.Join(in.A.Dir, cpA.File))
		bPNG, errB := os.ReadFile(filepath.Join(in.B.Dir, cpB.File))
		if errA != nil || errB != nil {
			s.Checkpoints = append(s.Checkpoints, CheckpointVerdict{Name: name, Verdict: "unreadable"})
			continue
		}
		masks := pixel.RectsFrom(in.Cfg.MasksFor(in.Flow, name))
		res, imgs, err := pixel.Compare(aPNG, bPNG, pixel.Options{
			Masks:         masks,
			GateThreshold: in.Cfg.Thresholds.Gate,
			FineThreshold: in.Cfg.Thresholds.Fine,
			WantDiff:      in.WantImages,
			WantOverlay:   in.WantImages,
			// Either side asking for a trim trims both — comparing a
			// trimmed shot against an untrimmed one would be a geometry
			// mismatch invented by the tool.
			Trim: cpA.Trim || cpB.Trim,
		})
		if err != nil {
			s.Checkpoints = append(s.Checkpoints, CheckpointVerdict{Name: name, Verdict: "unreadable"})
			continue
		}
		v := CheckpointVerdict{
			Name: name, DiffPct: res.DiffPct, DiffPctFine: res.DiffPctFine,
			NumDiff: res.NumDiff, Mismatch: res.Mismatch, Overlap: res.Overlap,
			Verdict: "ok",
		}
		if res.TrimA != nil || res.TrimB != nil {
			v.Trimmed = &TrimRects{A: res.TrimA, B: res.TrimB}
		}
		if res.DiffPct > in.Cfg.Thresholds.Gate || res.Mismatch {
			v.Verdict = "changed"
		}
		if in.WantImages {
			imgs2, ierr := writeCheckpointImages(in.OutDir, name, cpA, cpB, imgs)
			if ierr != nil {
				return Summary{}, fmt.Errorf("diff: Build: writing checkpoint images for %q: %w", name, ierr)
			}
			v.Images = imgs2
		}
		s.Checkpoints = append(s.Checkpoints, v)
	}

	// --- wire, from each side's client-edge hops
	hopsA, _, err := runs.ReadHops(filepath.Join(in.A.Dir, "wire.jsonl"))
	if err != nil {
		return Summary{}, fmt.Errorf("diff: Build: reading %s wire.jsonl: %w", in.A.Dir, err)
	}
	hopsB, _, err := runs.ReadHops(filepath.Join(in.B.Dir, "wire.jsonl"))
	if err != nil {
		return Summary{}, fmt.Errorf("diff: Build: reading %s wire.jsonl: %w", in.B.Dir, err)
	}
	s.Wire = DiffWire(hopsA, hopsB, in.Options)
	s.Sections = BuildSections(s.Wire.Paired, s.Wire.Groups)

	// --- hops, from the full chain; absent on a standalone run, and that
	// is reported as "not captured", never as "no differences".
	chainA, _, err := runs.ReadHops(filepath.Join(in.A.Dir, "hops.jsonl"))
	if err != nil {
		return Summary{}, fmt.Errorf("diff: Build: reading %s hops.jsonl: %w", in.A.Dir, err)
	}
	chainB, _, err := runs.ReadHops(filepath.Join(in.B.Dir, "hops.jsonl"))
	if err != nil {
		return Summary{}, fmt.Errorf("diff: Build: reading %s hops.jsonl: %w", in.B.Dir, err)
	}
	if chainA != nil || chainB != nil {
		// NoCollapse is deliberately not set: folding is on by default, and
		// the default is what every real run gets.
		s.Hops = DiffHops(chainA, chainB, HopOptions{
			Normalize: in.Cfg.NormalizePath,
			Expected:  in.Cfg.ExpectedStatuses,
			Require:   in.Cfg.HopRequire,
			// CountTolerance is left at zero → DefaultCountTolerance.
		})
	}

	// --- auxiliary checks always run against side B (the candidate).
	// hops.jsonl is a superset of wire.jsonl (the client edge is part of
	// the chain), so each call runs exactly once against the widest record
	// that exists.
	statusHops := chainB
	if statusHops == nil {
		statusHops = hopsB
	}
	s.UnexpectedStatuses = FindUnexpectedStatuses(statusHops, in.Cfg.ExpectedStatuses)
	s.Perf = CheckPerfBudget(hopsB, in.Cfg.Flows[in.Flow].PerfBudgetMs)
	if in.Cfg.OpenAPI != "" {
		s.Conformance, err = CheckOpenAPI(hopsB, filepath.Join(in.Cfg.Dir, in.Cfg.OpenAPI))
		if err != nil {
			return Summary{}, fmt.Errorf("diff: Build: checking OpenAPI conformance: %w", err)
		}
	}

	s.Counts = countOf(s)
	s.Gates = gatesOf(s)
	// budgetsOf builds one Gate per plane cfg.Gates configures — NEVER one
	// per plane that merely exists.
	s.Budgets = budgetsOf(s, in.Cfg)
	switch {
	case len(s.Gates) > 0 || failingBudget(s.Budgets, in.Cfg.FailOn):
		s.Verdict = "failed"
	case changed(s) || (len(s.Budgets) > 0 && anyFailed(s.Budgets)):
		s.Verdict = "changed"
	default:
		s.Verdict = "pass"
	}
	return s, nil
}

func countOf(s Summary) Counts {
	c := Counts{Checkpoints: len(s.Checkpoints)}
	for _, cp := range s.Checkpoints {
		if cp.Verdict != "ok" {
			c.PixelChanged++
		}
	}
	c.WirePaired = len(s.Wire.Paired)
	c.WireMissing = len(s.Wire.Missing)
	c.WireExtra = len(s.Wire.Extra)
	for _, e := range s.Wire.Paired {
		for _, cl := range e.Classes {
			switch cl {
			case "changed":
				c.WireChanged++
			case "moved":
				c.WireMoved++
			}
		}
		c.Violations += len(e.BodyViolations)
		for _, hd := range e.HeaderDiff {
			if hd.Type == "violation" {
				c.Violations++
			}
		}
	}
	c.HopNew = len(s.Hops.NewRoutes)
	c.HopGone = len(s.Hops.GoneRoutes)
	c.UnexpectedStatuses = len(s.UnexpectedStatuses)
	c.Conformance = len(s.Conformance)
	return c
}

// gatesOf derives the human-readable "failed" reasons that do NOT come
// from a configured Budgets entry — a rule Violation on either plane, a
// hopRequire failure, an unexpected status, an over-budget perf plane, or a
// fatal capture on either side. Budget-driven failures are handled
// separately by failingBudget, once Budgets exists.
func gatesOf(s Summary) []string {
	var out []string
	for _, e := range s.Wire.Paired {
		for _, fd := range e.BodyViolations {
			out = append(out, fmt.Sprintf("violation: %s %s body[%s] %s", e.Method, e.NormalizedPath, fd.Scope, fd.Path))
		}
		for _, hd := range e.HeaderDiff {
			if hd.Type == "violation" {
				out = append(out, fmt.Sprintf("violation: %s %s header[%s] %s", e.Method, e.NormalizedPath, hd.Scope, hd.Name))
			}
		}
	}
	for _, rf := range s.Hops.RequiredRouteFailures {
		out = append(out, fmt.Sprintf("hopRequire failed: %s %s (%s)", rf.Method, rf.Path, rf.Reason))
	}
	for _, sf := range s.UnexpectedStatuses {
		out = append(out, fmt.Sprintf("unexpected status %d: %s %s", sf.Status, sf.Method, sf.Path))
	}
	if s.Perf.Status == "over" {
		out = append(out, fmt.Sprintf("perf budget exceeded: %.0fms > %.0fms", s.Perf.MeasuredMs, s.Perf.BudgetMs))
	}
	if capture.Fatal(s.Capture.A) {
		out = append(out, fmt.Sprintf("capture side a is not trustworthy: %s", s.Capture.A.Summary))
	}
	if capture.Fatal(s.Capture.B) {
		out = append(out, fmt.Sprintf("capture side b is not trustworthy: %s", s.Capture.B.Summary))
	}
	return out
}

// changed reports the "changed" verdict rules other than budgets: a
// checkpoint not "ok", any wire delta, any hop delta, or a non-"unchecked"
// conformance finding.
func changed(s Summary) bool {
	for _, cp := range s.Checkpoints {
		if cp.Verdict != "ok" {
			return true
		}
	}
	if s.Counts.WireChanged > 0 || s.Counts.WireMoved > 0 || s.Counts.WireMissing > 0 || s.Counts.WireExtra > 0 {
		return true
	}
	if s.Counts.HopNew > 0 || s.Counts.HopGone > 0 {
		return true
	}
	for _, svc := range s.Hops.ServiceCounts {
		if svc.Deviates {
			return true
		}
	}
	for _, f := range s.Conformance {
		if f.Kind != "unchecked" {
			return true
		}
	}
	return false
}

// budgetsOf builds one Gate per plane cfg.Gates configures (never one per
// plane that merely exists — see Summary.Budgets' doc comment), in a fixed
// plane order so the result is deterministic regardless of Go's randomized
// map iteration order.
//
// Observed's derivation is spelled out for "pixel" (the worst
// per-checkpoint DiffPct) and "wire" (the wire plane's own change count,
// Counts.WireChanged — a raw count, matching the brief's literal wording,
// not a percentage normalized against WirePaired) by the brief itself.
// "hop" and "perf" are this implementer's documented judgment call, since
// the brief says only "and so on" and no listed test pins either: hop uses
// the percentage of ServiceCounts entries that deviate, and perf uses
// percent of budget consumed (MeasuredMs/BudgetMs*100, which lines up with
// PerfResult.Status == "over" at exactly 100).
func budgetsOf(s Summary, cfg *config.Config) []Gate {
	planes := []string{"hop", "perf", "pixel", "wire"}
	var out []Gate
	for _, plane := range planes {
		g, ok := cfg.Gates[plane]
		if !ok || g.BudgetPct == nil {
			continue
		}
		threshold := *g.BudgetPct
		observed := observedFor(s, plane)
		out = append(out, Gate{Plane: plane, Threshold: threshold, Observed: observed, Failed: observed > threshold})
	}
	return out
}

func observedFor(s Summary, plane string) float64 {
	switch plane {
	case "pixel":
		var worst float64
		for _, cp := range s.Checkpoints {
			if cp.DiffPct > worst {
				worst = cp.DiffPct
			}
		}
		return worst
	case "wire":
		return float64(s.Counts.WireChanged)
	case "hop":
		total := len(s.Hops.ServiceCounts)
		if total == 0 {
			return 0
		}
		deviating := 0
		for _, svc := range s.Hops.ServiceCounts {
			if svc.Deviates {
				deviating++
			}
		}
		return 100 * float64(deviating) / float64(total)
	case "perf":
		if s.Perf.BudgetMs == 0 {
			return 0
		}
		return 100 * s.Perf.MeasuredMs / s.Perf.BudgetMs
	default:
		return 0
	}
}

func failingBudget(budgets []Gate, failOn []string) bool {
	allowed := map[string]bool{}
	for _, p := range failOn {
		allowed[p] = true
	}
	for _, g := range budgets {
		if g.Failed && allowed[g.Plane] {
			return true
		}
	}
	return false
}

func anyFailed(budgets []Gate) bool {
	for _, g := range budgets {
		if g.Failed {
			return true
		}
	}
	return false
}

// ExitCode maps a Summary's Verdict to the CI contract: 0 pass, 1 changed,
// 2 failed, 3 quarantined (or, at the CLI layer, could-not-evaluate).
func ExitCode(s Summary) int {
	switch s.Verdict {
	case "quarantined":
		return 3
	case "failed":
		return 2
	case "changed":
		return 1
	}
	return 0
}

// RenderText prints the human-facing report. Wide values are never
// truncated — a report an agent must read is not a dashboard.
func RenderText(w io.Writer, s Summary) {
	if s.Verdict == "quarantined" {
		fmt.Fprintln(w, "QUARANTINED: this comparison was refused because a side's capture was not trusted")
		for _, q := range s.Quarantined {
			fmt.Fprintf(w, "  side %s: %s\n", q.Side, q.Reason)
		}
		return
	}

	if s.Capture.A.Status != trace.VerdictOK {
		fmt.Fprintf(w, "capture a: %s — %s\n", s.Capture.A.Status, s.Capture.A.Summary)
	}
	if s.Capture.B.Status != trace.VerdictOK {
		fmt.Fprintf(w, "capture b: %s — %s\n", s.Capture.B.Status, s.Capture.B.Summary)
	}

	for _, cp := range s.Checkpoints {
		mark := "✓"
		if cp.Verdict != "ok" {
			mark = "✗"
		}
		switch cp.Verdict {
		case "ok", "changed":
			line := fmt.Sprintf("%s %-8s %.2f%%", mark, cp.Name, cp.DiffPct)
			if cp.DiffPctFine != cp.DiffPct {
				line += fmt.Sprintf("  (fine %.2f%%)", cp.DiffPctFine)
			}
			if cp.Images.Diff != "" {
				line += "  " + cp.Images.Diff
			}
			fmt.Fprintln(w, line)
		default:
			fmt.Fprintf(w, "%s %-8s %s\n", mark, cp.Name, cp.Verdict)
		}
	}

	for _, sec := range s.Sections {
		name := sec.Name
		if name == "" {
			name = "(unnamed)"
		}
		fmt.Fprintf(w, "-- %s --\n", name)
		entries := append([]Entry(nil), sec.Entries...)
		sort.SliceStable(entries, func(i, j int) bool {
			return len(entries[i].BodyDiff)+len(entries[i].BodyViolations) > len(entries[j].BodyDiff)+len(entries[j].BodyViolations)
		})
		for _, e := range entries {
			fmt.Fprintf(w, "  %s %s %v\n", e.Method, e.NormalizedPath, e.Classes)
		}
	}
	for _, c := range s.Wire.Missing {
		fmt.Fprintf(w, "  MISSING %s %s\n", c.Method, c.Path)
	}
	for _, c := range s.Wire.Extra {
		fmt.Fprintf(w, "  EXTRA   %s %s\n", c.Method, c.Path)
	}

	for _, r := range s.Hops.NewRoutes {
		fmt.Fprintf(w, "  NEW ROUTE   %s %s %s\n", r.To, r.Method, r.Path)
	}
	for _, r := range s.Hops.GoneRoutes {
		fmt.Fprintf(w, "  GONE ROUTE  %s %s %s\n", r.To, r.Method, r.Path)
	}

	for _, g := range s.Gates {
		fmt.Fprintf(w, "GATE: %s\n", g)
	}
	for _, b := range s.Budgets {
		status := "ok"
		if b.Failed {
			status = "FAILED"
		}
		fmt.Fprintf(w, "BUDGET: %s %.2f%% → %.2f%% %s\n", b.Plane, b.Threshold, b.Observed, status)
	}

	fmt.Fprintf(w, "VERDICT: %s\n", s.Verdict)
}
