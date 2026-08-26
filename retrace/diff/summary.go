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
	"strings"

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
	// OpenAPIConfigured says whether a spec was configured for this run, so
	// an empty Conformance can be read. Without it, "no spec configured" and
	// "spec configured and every call conformed" are the same empty array —
	// and never-checked would read as checked-and-clean, which is exactly
	// the defect Task 9's "unchecked" finding kind exists to prevent, one
	// level up: at plane scale instead of finding scale.
	//
	// Stated rather than encoded in the absence of data. Same shape as
	// HopDiff.HopRequireConfigured.
	//
	// true implies the plane was actually checked on any non-quarantined
	// Summary: a spec that fails to load makes CheckOpenAPI error, Build
	// returns no Summary at all, and `retrace diff` exits 3. On a
	// QUARANTINED Summary it does not, because Build returns before any
	// plane is computed — but there every field is empty on purpose, which
	// Verdict says explicitly, and conformance is not special among them.
	OpenAPIConfigured bool          `json:"openApiConfigured"`
	Capture           CaptureBanner `json:"capture"`
	Counts            Counts        `json:"counts"`
	// Suppressions lists every tolerance that actually silenced a
	// difference in this run, with how many times each fired. It exists
	// because a clean report and a report whose rules hid everything look
	// identical from the outside — and the second is the one worth reading.
	// Rows that suppressed nothing are absent, so an empty array means the
	// verdict was earned rather than configured.
	Suppressions []Suppression `json:"suppressions"`
	Gates        []string      `json:"gates"` // human-readable reasons the verdict is "failed"
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
	// UnmeasuredGates names the planes `gates:` configures and Budgets has
	// NO row for: gated, and this run carried no evidence to measure them
	// against. It is the other half of Budgets' rule, and it lives on the
	// Summary because Budgets alone cannot express it — a plane nobody
	// gated and a plane gated-but-unmeasurable are the same absence, and
	// every consumer that read the absence alone reported "pass".
	//
	// Derived once, here, from the config's own keys and the Summary's own
	// rows; there is no second plane list to drift out of step with
	// budgetsOf's. Four surfaces consume it (text, --json, the review UI,
	// the static export) and none re-derives it — the static export
	// re-deriving it privately, correctly, while the other three stayed
	// silent is exactly how this field came to exist.
	//
	// A plane named here AND in fail_on is a verdict "failed" (see
	// unevaluatedGateReasons): a gate the user asked to break the build
	// which could not be evaluated is not a gate that passed.
	UnmeasuredGates []string `json:"unmeasuredGates"`
	// Quarantined lists the sides excluded from this comparison because
	// their own capture-trust verdict was not "ok". Empty unless
	// --allow-degraded was NOT passed and at least one side warranted it.
	Quarantined []Quarantine `json:"quarantined"`
	// Triage says WHOSE problem this is — the one question the four planes
	// leave to the reader. See triage.go; never empty on a Summary Build
	// returned, including both quarantine exits.
	Triage Triage `json:"triage"`
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
	// The deviations ledger. This is the ONE assembly point, so there is no
	// second place for a caller to forget it. A load failure is returned,
	// never swallowed: a config that names a ledger it cannot read must fail
	// the diff (exit 3), not run it with nothing tolerated.
	//
	// The app pair comes from the two MANIFESTS, not from a flag — the
	// deviation is a fact about the two apps actually being compared.
	if cfg.Deviations != "" {
		ds, err := LoadDeviations(filepath.Join(cfg.Dir, cfg.Deviations))
		if err != nil {
			return Options{}, err
		}
		o.Deviations = ResolveDeviations(ds, a.App, b.App)
	}
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

// incompleteCheck reports which sides of a and b never finished recording:
// `retrace run` execs the `-- <test command>` tail (cmd_run.go), and
// exec.ExitError.ExitCode() reports -1 (never any other negative value)
// specifically for a process terminated by a signal — CI's timeout or a
// Ctrl-C mid-test. cmd_run.go writes that raw code straight into
// runs.Test.ExitCode, so a negative value on disk means the hop stream is
// truncated at the moment of the kill, not that anything was verified.
// Diffing a truncated stream against a complete reference would report
// every un-run hop as a fabricated "gone route"/"missing call" — the same
// false-positive class Task 9's C1 review found for nondeterministic path
// matching, just from stale data instead of stale ordering.
//
// Deliberately does NOT key off Test.ExitCode == 0: a manifest that omits
// `test.exitCode` entirely is indistinguishable, after JSON decode, from
// one recording a genuine passing 0 — the same overloaded-zero trap this
// whole plan keeps finding. Checking only "< 0" sidesteps it: Go's
// ExitCode() never returns any other negative number, so this check reads
// as unambiguous evidence either way, unlike the zero value would.
//
// Unlike quarantineCheck below, this check is NOT gated behind
// AllowDegraded: --allow-degraded exists so a human can accept a
// LOWER-CONFIDENCE but still-complete capture (a quiet stretch, a capture
// note); a run that never finished has no complete data to accept, so
// there is nothing for that flag to override.
func incompleteCheck(a, b RunRef) []Quarantine {
	var out []Quarantine
	if code := a.Manifest.Test.ExitCode; code < 0 {
		out = append(out, Quarantine{Side: "a", Reason: fmt.Sprintf(
			"the test command did not complete (signal-killed, raw exit code %d) — the recording is truncated, not a comparable run", code)})
	}
	if code := b.Manifest.Test.ExitCode; code < 0 {
		out = append(out, Quarantine{Side: "b", Reason: fmt.Sprintf(
			"the test command did not complete (signal-killed, raw exit code %d) — the recording is truncated, not a comparable run", code)})
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
	// "none" is the refusing value of the Kind vocabulary: it means "I
	// could not compare", never "nothing differed". Building from one would
	// produce a Summary whose every plane is empty and whose verdict is
	// therefore clean — the plausible-value trap, in the one document every
	// consumer reads.
	//
	// The callers that resolve a reference each guard already, and those
	// guards STAY: they own the operator-facing message that names
	// `retrace ref accept`, which this error cannot produce because Build
	// does not know how the side was resolved. This is not redundancy — it
	// is the invariant a consumer that does not exist yet (Task 12's replay
	// server, Task 13's review server) cannot fail to inherit. A rule
	// re-implemented at each consumer is a rule that will be forgotten at
	// the next one.
	for _, side := range []struct {
		label string
		ref   RunRef
	}{{"A", in.A}, {"B", in.B}} {
		if side.ref.Kind == "none" {
			return Summary{}, fmt.Errorf("side %s resolved to nothing comparable (kind %q, reason recorded by whoever resolved it) — there is no comparison to report, and reporting one would say \"nothing differed\" about a diff that never ran; resolve it, or tell the operator why it could not be resolved", side.label, side.ref.Kind)
		}
	}
	s := Summary{Schema: SummarySchema, App: in.App, Flow: in.Flow, A: in.A, B: in.B}
	s.Capture = CaptureBanner{A: in.A.Manifest.Capture, B: in.B.Manifest.Capture}
	// Set BEFORE the quarantine exits: this is a fact about configuration,
	// not about what got computed, and reporting false here for a run that
	// did configure a spec would be untrue.
	s.OpenAPIConfigured = in.Cfg.OpenAPI != ""

	// --- incomplete, checked first and unconditionally (not even
	// --allow-degraded gets past it — see incompleteCheck's doc comment). A
	// signal-killed test command leaves a truncated recording, which is not
	// evidence of anything, degraded or otherwise.
	if q := incompleteCheck(in.A, in.B); len(q) > 0 {
		s.Quarantined = q
		s.Verdict = "quarantined"
		s.finish(in.Cfg)
		return s, nil
	}

	// --- quarantine, checked before anything else is compared. A capture
	// whose own trust verdict is not "ok" makes every downstream comparison
	// confident nonsense, so this returns immediately rather than computing
	// a partial Summary. --allow-degraded is the only way past it.
	if !in.AllowDegraded {
		if q := quarantineCheck(in.A, in.B); len(q) > 0 {
			s.Quarantined = q
			s.Verdict = "quarantined"
			s.finish(in.Cfg)
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
	// ...and unmeasuredGatesOf names the planes budgetsOf REFUSED to emit a
	// row for. The refusal is right; reading the resulting absence as "no
	// gate failed" is what made an unevaluatable gate exit 0.
	s.UnmeasuredGates = unmeasuredGatesOf(s, in.Cfg)
	s.Gates = append(s.Gates, unevaluatedGateReasons(s.UnmeasuredGates, in.Cfg.FailOn)...)
	switch {
	case len(s.Gates) > 0 || failingBudget(s.Budgets, in.Cfg.FailOn):
		s.Verdict = "failed"
	case changed(s) || (len(s.Budgets) > 0 && anyFailed(s.Budgets)):
		s.Verdict = "changed"
	default:
		s.Verdict = "pass"
	}
	s.finish(in.Cfg)
	return s, nil
}

// finish is the ONE exit ritual every return path in Build shares: normalise
// the arrays, then classify. Both quarantine returns and the ordinary return
// go through it, so a fifth exit added later inherits both steps instead of
// silently shipping a Summary whose triage label is the empty string.
//
// Ordered deliberately — triageOf reads Verdict and Counts, which are set by
// the time any caller reaches here, and reads Quarantined, which ensureArrays
// may have just turned from nil into an empty slice. len() is the same for
// both, so the order is not load-bearing today; it is fixed anyway so that a
// future signal keyed on nil-ness cannot depend on which call ran first.
func (s *Summary) finish(cfg *config.Config) {
	s.ensureArrays()
	// After ensureArrays, which seeds the field with an empty slice, and
	// before nothing in particular — triage does not read suppressions.
	s.Suppressions = suppressionsOf(*s, cfg)
	s.Triage = triageOf(*s, cfg)
}

// untolerated counts the calls in cs that no approved deviation covers.
func untolerated(cs []Call) int {
	n := 0
	for _, c := range cs {
		if c.Tolerated == nil {
			n++
		}
	}
	return n
}

func countOf(s Summary) Counts {
	c := Counts{Checkpoints: len(s.Checkpoints)}
	for _, cp := range s.Checkpoints {
		if cp.Verdict != "ok" {
			c.PixelChanged++
		}
	}
	c.WirePaired = len(s.Wire.Paired)
	// A call an approved deviation covers stays in Wire.Missing/Wire.Extra —
	// visible to every consumer, annotated with why — but does not count as
	// a finding. Counting it here is what would reach the verdict through
	// changed(), which is the one thing "tolerated" is supposed to stop.
	c.WireMissing = untolerated(s.Wire.Missing)
	c.WireExtra = untolerated(s.Wire.Extra)
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
// Observed is a PERCENTAGE for every plane, one unit across the whole map —
// Threshold always comes from `gates: {<plane>: {budget_pct}}`, so mixing
// units would compare a percentage against a raw count for whichever plane
// got it wrong (the team lead's review caught an earlier draft doing
// exactly that for "wire": 3 changed entries out of 1000 would have failed
// a budget_pct: 2 gate under a raw-count reading, when 0.3% plainly should
// not — see TestAWireBudgetComparesAPercentageNotARawCount, the fixture
// that pins the distinction; a same-verdict fixture would pin neither
// reading).
//
//   - pixel: the worst per-checkpoint DiffPct (already a %).
//   - wire: changed paired entries / total paired entries × 100.
//   - hop: % of ServiceCounts entries with Deviates == true.
//   - perf: percent OVER budget, (MeasuredMs-BudgetMs)/BudgetMs×100 — 0
//     means "exactly at budget", budget_pct: 10 means "10% over is
//     allowed". MeasuredMs/BudgetMs×100 (percent OF budget) was this
//     implementer's original call and was overturned in review: under it,
//     100 means "at budget" and every threshold on this one plane would
//     have to be written around 100, unlike the other three.
func budgetsOf(s Summary, cfg *config.Config) []Gate {
	planes := []string{"hop", "perf", "pixel", "wire"}
	var out []Gate
	for _, plane := range planes {
		g, ok := cfg.Gates[plane]
		if !ok || g.BudgetPct == nil {
			continue
		}
		// An UNMEASURABLE plane gets no Gate — the same rule, and now the
		// same code path, as a plane cfg.Gates never mentions. observedFor
		// divides for three of the four planes, and an empty denominator
		// is "I have no data", not "0% changed": a wire plane that paired
		// nothing, a hop plane with no ServiceCounts, and a perf plane
		// with no BudgetMs would each otherwise report a CLEAN gate on the
		// run with the least evidence in it. Perf already refused to emit
		// one; wire and hop returned a reassuring zero instead, which is
		// the zero-value constraint's third clause exactly — a plausible
		// value is worse than an empty one, because it sails through every
		// downstream seam. There is exactly ONE guard now, inside
		// observedFor, rather than a skip here and a defensive early
		// return there that could drift apart.
		observed, measurable := observedFor(s, plane)
		if !measurable {
			continue
		}
		threshold := *g.BudgetPct
		out = append(out, Gate{Plane: plane, Threshold: threshold, Observed: observed, Failed: observed > threshold})
	}
	return out
}

// observedFor derives one plane's Observed percentage AND reports whether
// that plane is measurable at all. The second return is not a convenience:
// three of the four planes divide, and "0, false" versus "0, true" is the
// difference between "nothing was measured" and "measured, and nothing
// changed" — which a bare float64 cannot carry. Returning a bare 0 for an
// empty denominator is what made a run that paired no wire entries report a
// clean wire budget.
//
// pixel does not divide: it is the worst per-checkpoint DiffPct, a max over
// a set, so it has no denominator to be empty.
func observedFor(s Summary, plane string) (observed float64, measurable bool) {
	switch plane {
	case "pixel":
		// A max over nothing is 0, which reads as "no pixels changed" — the
		// same false reassurance an empty denominator gives the other three
		// planes. Pixel does not divide, but the rule is about evidence, not
		// about division: no checkpoints, no gate.
		if len(s.Checkpoints) == 0 {
			return 0, false
		}
		var worst float64
		for _, cp := range s.Checkpoints {
			if cp.DiffPct > worst {
				worst = cp.DiffPct
			}
		}
		return worst, true
	case "wire":
		if s.Counts.WirePaired == 0 {
			return 0, false
		}
		return 100 * float64(s.Counts.WireChanged) / float64(s.Counts.WirePaired), true
	case "hop":
		total := len(s.Hops.ServiceCounts)
		if total == 0 {
			return 0, false
		}
		deviating := 0
		for _, svc := range s.Hops.ServiceCounts {
			if svc.Deviates {
				deviating++
			}
		}
		return 100 * float64(deviating) / float64(total), true
	case "perf":
		// BudgetMs == 0 means DerivePerfBudget never configured one: there
		// is no budget for a percentage to be OVER, so the plane is
		// unmeasurable rather than "0% over budget: clean".
		if s.Perf.BudgetMs == 0 {
			return 0, false
		}
		// ...and a budget with nothing to measure against it is the same
		// empty-evidence case the other three planes already refuse, which
		// this one did not: MeasuredMs is TotalCallDurationMs over side
		// B's hops, so a run that recorded NO calls measures 0ms, lands at
		// -100% of any budget, and reported a CLEAN gate on the run with
		// the least evidence in it — the exact sentence budgetsOf's own
		// comment claims cannot happen. With fail_on: [perf] that is a
		// green CI job over a run that made no calls.
		//
		// The test is the CALL COUNT, not MeasuredMs == 0: a genuinely
		// fast run can measure at or near zero and must still be gated.
		// Paired + Extra is exactly side B's hops — every hop in hopsB
		// lands in one of the two — so it counts the same calls
		// TotalCallDurationMs summed. Extra is read with len() rather than
		// through Counts.WireExtra, which subtracts tolerated calls: a
		// tolerated call still took time and is still evidence.
		if len(s.Wire.Paired)+len(s.Wire.Extra) == 0 {
			return 0, false
		}
		return 100 * (s.Perf.MeasuredMs - s.Perf.BudgetMs) / s.Perf.BudgetMs, true
	default:
		return 0, false
	}
}

// unmeasuredGatesOf names the planes cfg gates and s has no Budget row for,
// sorted so the result is deterministic regardless of Go's randomized map
// iteration order.
//
// It reads the CONFIG's own keys and the Summary's own rows rather than a
// third plane list, so it cannot fall out of step with budgetsOf: whatever
// budgetsOf declines to emit for a configured plane lands here, including
// any plane either function gains later.
//
// Note that budgetsOf skips a plane for TWO reasons — `gates:` never named
// it, and `gates:` named it but observedFor found no evidence — and only
// the second belongs here. The `g.BudgetPct == nil` test is what separates
// them: it is the same test budgetsOf uses to decide the plane is
// configured at all.
func unmeasuredGatesOf(s Summary, cfg *config.Config) []string {
	if cfg == nil {
		return nil
	}
	measured := map[string]bool{}
	for _, g := range s.Budgets {
		measured[g.Plane] = true
	}
	var out []string
	for plane, g := range cfg.Gates {
		if g.BudgetPct == nil || measured[plane] {
			continue
		}
		out = append(out, plane)
	}
	sort.Strings(out)
	return out
}

// unevaluatedGateReasons turns "gated, unmeasurable, and named in fail_on"
// into a Gates reason — which is to say, into verdict "failed" and exit 2.
//
// The fail_on scoping is deliberate and mirrors failingBudget's: fail_on is
// the project's own statement of which planes may break the build, and a
// plane outside it does not break the build whether it was measured or not.
// The auto-inserted pixel gate (config.applyDefaults) is the case this
// protects — every screenshot-less flow carries one, and turning those into
// failures would punish projects that never asked pixel to gate anything.
// Such a plane is still NAMED on every surface via UnmeasuredGates; it is
// reported, just not fatal.
//
// Exit 2 rather than 3: exit 3 means the comparison itself is unusable
// (quarantine — Build returns before any plane is computed). Here the
// comparison is entirely usable and one configured gate is not; reporting
// it as 3 would tell CI to discard findings the other planes did produce.
func unevaluatedGateReasons(unmeasured []string, failOn []string) []string {
	allowed := map[string]bool{}
	for _, p := range failOn {
		allowed[p] = true
	}
	var out []string
	for _, plane := range unmeasured {
		if !allowed[plane] {
			continue
		}
		out = append(out, fmt.Sprintf("gate not evaluated: %s is gated and named in fail_on, but this run carried no evidence to measure it against — that is not a gate that passed", plane))
	}
	return out
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
		// Printed on this path too. A quarantine is the one verdict readers
		// most often mistake for a small failure and go looking for in their
		// application code; "TRIAGE: harness" is the line that redirects them.
		renderTriage(w, s.Triage)
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
	// Printed after the BUDGET rows and before the verdict, because it is
	// read as one list with them: these are the gates this project
	// configured whose rows are absent above. Without this line the reader
	// infers "no BUDGET row for wire" means "wire is not gated", which is a
	// claim about configuration they cannot check from the report.
	for _, plane := range s.UnmeasuredGates {
		fmt.Fprintf(w, "BUDGET: %s NOT EVALUATED — gated by this project's config, and this run carried no evidence to measure it against. That is not a gate that passed.\n", plane)
	}

	renderConformance(w, s.Conformance)

	renderSuppressions(w, s.Suppressions)
	renderTriage(w, s.Triage)
	fmt.Fprintf(w, "VERDICT: %s\n", s.Verdict)
}

// renderSuppressions prints the tolerances that fired, immediately before
// the verdict. Position is the point: a clean verdict and a verdict its own
// rules bought look the same, and this is the line that tells them apart
// for someone reading a CI log rather than the JSON.
//
// Nothing is printed when nothing fired — an empty heading would train the
// reader to skip the section on the runs where it matters.
func renderSuppressions(w io.Writer, ss []Suppression) {
	if len(ss) == 0 {
		return
	}
	total := 0
	for _, s := range ss {
		total += s.Count
	}
	fmt.Fprintf(w, "SUPPRESSED: %d difference(s) across %d rule(s)\n", total, len(ss))
	for _, s := range ss {
		fmt.Fprintf(w, "  %-6s %-24s %-11s %-12s ×%d\n", s.Plane, s.Target, s.Source, s.Matcher, s.Count)
		// The why on its own line, indented under the row it explains,
		// rather than appended to it. Reasons are sentences and the row is a
		// fixed-width table: inlining one would blow the column alignment
		// for every row below it, and the alignment is what makes the table
		// scannable. A tolerance with no why prints nothing extra — never a
		// "(no reason given)" placeholder, which would read as config the
		// project wrote.
		if strings.TrimSpace(s.Why) != "" {
			fmt.Fprintf(w, "         ↳ %s\n", s.Why)
		}
	}
}

// triageAdvice is the one-line "so what" for each built-in label. A project
// label has none, and gets the line without it rather than a fabricated one.
var triageAdvice = map[string]string{
	TriageHarness:        "the recording is not trustworthy — fix the capture, not the code",
	TriageClientBehavior: "the client sent something different",
	TriageStack:          "the client sent the same requests; the stack answered differently",
	TriageContractDrift:  "traffic is unchanged, so the spec moved",
	TriageClientUI:       "a rendering change, with no traffic change",
	TriageNone:           "nothing moved",
	TriageUnclassified:   "nothing the triage signals cover moved — read GATE: above for what failed",
}

// renderTriage prints the classification and the signal vector behind it. The
// vector is printed, not just the label, for the same reason it is on the
// wire: a label the reader cannot check against the evidence is a label they
// have to take on faith.
func renderTriage(w io.Writer, t Triage) {
	if t.Label == "" {
		return // a hand-built Summary that never went through Build
	}
	line := fmt.Sprintf("TRIAGE: %s (%s)", t.Label, t.Rule)
	if advice := triageAdvice[t.Label]; advice != "" {
		line += " — " + advice
	}
	fmt.Fprintln(w, line)
	var moved []string
	for _, s := range []struct {
		name  string
		moved bool
	}{
		{"capture", t.Signals.Capture}, {"wire", t.Signals.Wire}, {"hop", t.Signals.Hop},
		{"spec", t.Signals.Spec}, {"pixel", t.Signals.Pixel},
	} {
		if s.moved {
			moved = append(moved, s.name)
		}
	}
	if len(moved) == 0 {
		fmt.Fprintln(w, "  signals moved: none")
		return
	}
	fmt.Fprintf(w, "  signals moved: %s\n", strings.Join(moved, ", "))
}

// renderConformance prints the conformance section of the human-facing
// report, with "unchecked" findings on their own labelled lines.
//
// Task 9 added ConformanceFinding.Kind "unchecked" for exactly one reason:
// an unresolvable $ref, a body that fails json.Unmarshal, or a body the
// Redactor truncated at capture must NEVER read as a verified pass. Until
// this function existed, --json carried that finding and RenderText did
// not — so in the default human-facing view, everything that is not
// --json, an unchecked finding was invisible and "VERDICT: pass" was the
// whole story. That is a producer fixed and a consumer unwritten, which
// puts the silent pass back one layer down.
//
// Nothing is printed when there are no findings. "CONFORMANCE: 0 findings"
// would be the same reassuring-value trap in a new costume: a clean-looking
// line on a run where no spec was configured and no check ever ran. An
// absent section says "nothing to report"; it does not claim "verified".
func renderConformance(w io.Writer, findings []ConformanceFinding) {
	if len(findings) == 0 {
		return
	}
	unchecked := 0
	for _, f := range findings {
		if f.Kind == "unchecked" {
			unchecked++
		}
	}
	header := fmt.Sprintf("CONFORMANCE: %d finding(s)", len(findings))
	if unchecked > 0 {
		header += fmt.Sprintf(", %d unchecked — an unchecked finding was NOT verified and is not a pass", unchecked)
	}
	fmt.Fprintln(w, header)
	for _, f := range findings {
		// The Kind leads the line, upper-cased, so UNCHECKED reads
		// differently from every real violation at a glance and cannot be
		// mistaken for one — nor for the silence of a pass.
		fmt.Fprintf(w, "  %-22s %s %s %d — %s\n", strings.ToUpper(f.Kind), f.Method, f.Path, f.Status, f.Detail)
	}
}

// ensureArrays gives every array-valued field on the wire type an empty
// slice rather than nil, so each one marshals as `[]` and never as `null`.
//
// null, absent and [] are three encodings of ONE meaning here — "no
// entries" — and the nil arrived by too many routes to mean anything on its
// own: budgetsOf returns nil both when no gates were configured and when
// gates were configured and none were measurable. Carrying that ambiguity
// on the wire buys nothing and costs every consumer a null-guard, where the
// consumer who forgets does not misbehave quietly, it crashes:
// `summary.budgets.map(...)` throws on an API-only flow, which is an
// ordinary correct configuration, not an edge case.
//
// Called at all three of Build's exits that return a completed Summary: the
// ordinary exit and both quarantine exits (quarantineCheck's untrusted
// capture, incompleteCheck's truncated recording). The quarantine returns
// matter as much as the normal one: those paths compute almost nothing, so
// they are the exits most likely to hand a consumer a nil, and "we refused
// to compare" is already carried explicitly by Verdict — it does not also
// need to be encoded in the shape of nine other fields.
//
// Conformance IS in this list, flattened like every other plane — see the
// comment at its assignment below for what carries the "was this plane
// even checked" distinction instead of the null/[] difference.
func (s *Summary) ensureArrays() {
	if s.Checkpoints == nil {
		s.Checkpoints = []CheckpointVerdict{}
	}
	if s.Sections == nil {
		s.Sections = []Section{}
	}
	for i := range s.Sections {
		if s.Sections[i].Entries == nil {
			s.Sections[i].Entries = []Entry{}
		}
	}
	if s.UnexpectedStatuses == nil {
		s.UnexpectedStatuses = []StatusFinding{}
	}
	if s.Gates == nil {
		s.Gates = []string{}
	}
	if s.Budgets == nil {
		s.Budgets = []Gate{}
	}
	if s.Suppressions == nil {
		s.Suppressions = []Suppression{}
	}
	if s.UnmeasuredGates == nil {
		s.UnmeasuredGates = []string{}
	}
	if s.Quarantined == nil {
		s.Quarantined = []Quarantine{}
	}
	// Flattened like every other plane. What an empty Conformance MEANS is
	// carried by OpenAPIConfigured, which states the fact instead of hiding
	// it in the difference between null and [].
	if s.Conformance == nil {
		s.Conformance = []ConformanceFinding{}
	}

	// runs.Manifest rides along inside Summary.A/B, so its arrays are part
	// of this contract — but a manifest is a PERSISTED artifact. Retagging
	// it would fix only runs recorded from today and leave every bundle
	// already on disk decoding to nil, so it is normalised here, on the
	// Summary side, where old and new manifests are fixed alike and no
	// stored format changes.
	for _, m := range []*runs.Manifest{&s.A.Manifest, &s.B.Manifest} {
		if m.Checkpoints == nil {
			m.Checkpoints = []runs.Checkpoint{}
		}
		if m.Groups == nil {
			m.Groups = []runs.Group{}
		}
	}

	if s.Wire.Paired == nil {
		s.Wire.Paired = []Entry{}
	}
	// An UNCHANGED paired call is the most common Entry in any summary, and
	// it is the one where all seven of these are empty — so this is the
	// highest-traffic row the always-arrays rule has to cover, not an edge
	// case. Entries live in two places on the Summary and both are walked.
	//
	// Both loops are load-bearing, on every run. buildSection (order.go)
	// copies its entries into a fresh backing array unconditionally, so
	// Sections[i].Entries[j] and Wire.Paired[k] are always separate memory
	// — never the same struct, on a grouped run or an ungrouped one.
	// Dropping either loop leaves the arm it owns unnormalised: six of its
	// seven array keys marshal back to null. An earlier version of this
	// comment claimed dropping either loop was an equivalent mutant because
	// the two sides aliased — that was only ever true pre-copy, and only on
	// the shape (ungrouped) that every fixture happened to build; see
	// TestAnUnchangedPairedCallShipsEveryArrayKeyThroughBuild's grouped and
	// ungrouped cases for both proofs.
	for i := range s.Wire.Paired {
		ensureEntryArrays(&s.Wire.Paired[i])
	}
	for i := range s.Sections {
		for j := range s.Sections[i].Entries {
			ensureEntryArrays(&s.Sections[i].Entries[j])
		}
	}
	if s.Wire.Missing == nil {
		s.Wire.Missing = []Call{}
	}
	if s.Wire.Extra == nil {
		s.Wire.Extra = []Call{}
	}
	// Wire.Groups stays a nil POINTER when neither manifest named any
	// groups. That nil is a real distinction — "this flow has no group
	// structure" is not "this flow has groups and they are empty" — and it
	// is carried by a pointer, which is the honest way to say "absent",
	// rather than by the emptiness of an array.
	if s.Wire.Groups != nil {
		if s.Wire.Groups.A == nil {
			s.Wire.Groups.A = []string{}
		}
		if s.Wire.Groups.B == nil {
			s.Wire.Groups.B = []string{}
		}
	}

	if s.Hops.ServiceCounts == nil {
		s.Hops.ServiceCounts = []ServiceCount{}
	}
	if s.Hops.NewRoutes == nil {
		s.Hops.NewRoutes = []Route{}
	}
	if s.Hops.GoneRoutes == nil {
		s.Hops.GoneRoutes = []Route{}
	}
	if s.Hops.NewErrors == nil {
		s.Hops.NewErrors = []StatusFinding{}
	}
	if s.Hops.GoneErrors == nil {
		s.Hops.GoneErrors = []StatusFinding{}
	}
	if s.Hops.RequiredRouteFailures == nil {
		s.Hops.RequiredRouteFailures = []RouteFailure{}
	}
	for i := range s.Hops.NewRoutes {
		if s.Hops.NewRoutes[i].Via == nil {
			s.Hops.NewRoutes[i].Via = []string{}
		}
	}
	for i := range s.Hops.GoneRoutes {
		if s.Hops.GoneRoutes[i].Via == nil {
			s.Hops.GoneRoutes[i].Via = []string{}
		}
	}
}

// ensureEntryArrays is the Entry half of ensureArrays. Separate because an
// Entry appears in two places on the Summary — Wire.Paired and every
// Section's Entries — and one body for both is the only way they cannot
// drift apart.
func ensureEntryArrays(e *Entry) {
	if e.Classes == nil {
		e.Classes = []string{}
	}
	if e.BodyDiff == nil {
		e.BodyDiff = []FieldDiff{}
	}
	if e.BodyTolerated == nil {
		e.BodyTolerated = []FieldDiff{}
	}
	if e.BodyViolations == nil {
		e.BodyViolations = []FieldDiff{}
	}
	if e.BodyIgnored == nil {
		e.BodyIgnored = []FieldDiff{}
	}
	if e.OrderingChanges == nil {
		e.OrderingChanges = []FieldDiff{}
	}
	if e.HeaderDiff == nil {
		e.HeaderDiff = []HeaderDiff{}
	}
	if e.HeaderIgnored == nil {
		e.HeaderIgnored = []HeaderDiff{}
	}
}
