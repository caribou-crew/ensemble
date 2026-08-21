// Package refs owns the reference bundle: the committed artifact a diff
// runs against, how a flow resolves to one, and how a run is promoted into
// one (accept) or captured as a repro bundle when it is rejected.
//
// A bundle lives at <cwd>/.retrace-ref/<app>/<flow>/reference and IS
// committed to git — that, not a separate proposal tree with a bless
// ceremony, is what makes an agent accepting the wrong thing visible: it
// arrives as a reviewable diff in a pull request rather than as an
// invisible state change. The run-id level is the literal string
// runs.RefRunID ("reference"), never the source run's id, so a promotion
// shows up in git as a screenshot MODIFIED rather than one directory
// deleted and another added.
package refs

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/retrace/diff"
	"github.com/caribou-crew/ensemble/retrace/diff/pixel"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

// MaxBundleBytes caps one bundle, enforced at accept. A reference bundle is
// committed, so its size is a repository cost every clone pays forever.
const MaxBundleBytes = 8 << 20 // 8 MiB per bundle

// BundleDir is the reference bundle's directory for one app/flow.
//
// It returns (string, error), not a bare string, because it is a path
// CONSTRUCTOR and a constructor has no natural empty: returning "" would
// invite a caller into filepath.Join("", ...) and a path rooted at the
// process CWD. The listers in retrace/runs (ListFlows, FindRun) fail closed
// to an empty result because "nothing found" is a safe answer for a lister;
// this is the PathsFor shape instead, and PathsFor returns an error too.
//
// app and flow go through runs.ValidateComponents — the same single guard
// body PathsFor uses, delegated, never copied here. This is the second
// package in the tree to build an <app>/<flow> path, which is exactly the
// condition Task 1's re-review predicted would reintroduce the traversal
// bug; one guard body is the whole rule.
func BundleDir(cwd, app, flow string) (string, error) {
	if err := runs.ValidateComponents(app, flow); err != nil {
		return "", err
	}
	return filepath.Join(runs.RefsRoot(cwd), app, flow, runs.RefRunID), nil
}

// Candidate is one run Resolve considered as a reference, and what it
// decided. Every run examined gets an entry, eligible or not: an empty
// state that says only "no reference" is useless, and a History that is
// present but empty on a flow that HAS runs reads as "there were no
// candidates" when the truth is "there were five and all five were dirty".
type Candidate struct {
	RunID    string `json:"runId"`
	Eligible bool   `json:"eligible"`
	Reason   string `json:"reason"`
	Detail   string `json:"detail,omitempty"`
}

// Reference is what one app/flow resolves to.
type Reference struct {
	// Kind is the SAME vocabulary as diff.RunRef.Kind: "bundle" | "run" |
	// "none". Task 10's cmd_diff maps a Reference onto a RunRef by copying
	// this field through unchanged — there is no translation table, because
	// there are no two vocabularies to translate between.
	//
	// "none" means "I could not compare", never "nothing differed". Every
	// consumer must treat it as an inability to run, not as a clean result.
	Kind     string        `json:"kind"`
	Dir      string        `json:"dir"`
	RunID    string        `json:"runId"`
	Manifest runs.Manifest `json:"manifest"`
	Reason   string        `json:"reason,omitempty"`
	History  []Candidate   `json:"history,omitempty"`
}

// captureProbe is the minimum needed to classify a manifest that
// runs.ReadManifest will REFUSE. ReadManifest rejects an empty capture
// status (a manifest predating the verdict, or a hand-edited bundle — and
// bundles are committed, so hand-editing is expected), which would
// otherwise surface here as an undifferentiated "unreadable manifest". The
// probe exists so that case gets the reason it deserves: unknown capture is
// not ok.
type captureProbe struct {
	Capture struct {
		Status  trace.Verdict `json:"status"`
		Summary string        `json:"summary"`
	} `json:"capture"`
}

// candidateFor decides one run's eligibility, in the same order flowlens'
// resolveReference used, minus the git-ancestor check (that needed a
// configured trunk name and a git relationship the Go side does not have;
// here Git.Dirty == false plus a non-fatal capture verdict is the bar, and
// the reason strings say so). It returns the manifest only when eligible.
func candidateFor(manifestPath, runID string) (runs.Manifest, Candidate) {
	b, err := os.ReadFile(manifestPath)
	if err != nil {
		reason, detail := "no manifest — it did not finish", ""
		if !errors.Is(err, fs.ErrNotExist) {
			reason, detail = "unreadable manifest", err.Error()
		}
		return runs.Manifest{}, Candidate{RunID: runID, Reason: reason, Detail: detail}
	}
	var probe captureProbe
	if uerr := json.Unmarshal(b, &probe); uerr != nil {
		return runs.Manifest{}, Candidate{RunID: runID, Reason: "unreadable manifest", Detail: uerr.Error()}
	}
	// Checked BEFORE the strict read, so an unassessed capture is reported
	// as an unassessed capture rather than as a generic parse refusal. The
	// zero trace.Verdict ranks equal to VerdictOK in Verdict.Worse, so
	// "" must be named and refused here, not defaulted.
	if probe.Capture.Status != trace.VerdictOK {
		status, detail := string(probe.Capture.Status), probe.Capture.Summary
		if status == "" {
			status = "unknown"
			detail = "a run predating the verdict cannot vouch for itself"
		}
		return runs.Manifest{}, Candidate{RunID: runID, Reason: "capture " + status, Detail: detail}
	}
	m, err := runs.ReadManifest(manifestPath)
	if err != nil {
		return runs.Manifest{}, Candidate{RunID: runID, Reason: "unreadable manifest", Detail: err.Error()}
	}
	if m.Git.Dirty {
		return runs.Manifest{}, Candidate{RunID: runID, Reason: "dirty tree, not reproducible from a sha"}
	}
	return m, Candidate{RunID: runID, Eligible: true}
}

// Resolve answers what one flow compares against: the committed bundle
// first, then the newest eligible local run, then nothing — always with a
// reason, and on the "none" path with the full list of runs it tried.
//
// runsRoot is passed separately from cwd because the two roots are
// genuinely independent: bundles live under <cwd>/.retrace-ref while runs
// live wherever the caller recorded them.
func Resolve(cwd, runsRoot, app, flow string) Reference {
	dir, err := BundleDir(cwd, app, flow)
	if err != nil {
		return Reference{Kind: "none", Reason: err.Error()}
	}
	bundleManifest := filepath.Join(dir, "manifest.json")
	m, berr := runs.ReadManifest(bundleManifest)
	switch {
	case berr == nil:
		// RunID keeps the PROVENANCE of the run the bundle was promoted
		// from; the directory is the literal "reference".
		return Reference{Kind: "bundle", Dir: dir, RunID: m.RunID, Manifest: m}
	case !errors.Is(berr, fs.ErrNotExist):
		// The bundle EXISTS and cannot be read. Falling through to a local
		// run here would silently compare against something other than what
		// is committed — the operator would see "run" and never learn that
		// the artifact in git is broken. A bundle is hand-editable by
		// construction (it is committed), so this is a reachable state and
		// it is refused loudly.
		return Reference{Kind: "none", Reason: fmt.Sprintf(
			"the committed reference bundle at %s cannot be read: %v — fix or delete it; falling back to a local run would compare against something other than what is in git", dir, berr)}
	}

	ids, err := runs.ListRunsErr(runsRoot, app, flow)
	if err != nil {
		return Reference{Kind: "none", Reason: fmt.Sprintf("cannot read the runs root for %s/%s: %v", app, flow, err)}
	}
	if len(ids) == 0 {
		return Reference{Kind: "none", Reason: fmt.Sprintf("no runs captured for %s/%s", app, flow)}
	}

	// Newest first (run ids are timestamp-first, so lexical order IS
	// chronological order), stopping at the first eligible run.
	var history []Candidate
	for i := len(ids) - 1; i >= 0; i-- {
		p, perr := runs.PathsFor(runsRoot, app, flow, ids[i])
		if perr != nil {
			history = append(history, Candidate{RunID: ids[i], Reason: "invalid run id", Detail: perr.Error()})
			continue
		}
		m, c := candidateFor(p.ManifestPath, ids[i])
		history = append(history, c)
		if c.Eligible {
			return Reference{Kind: "run", Dir: p.RunDir, RunID: ids[i], Manifest: m, History: history}
		}
	}
	return Reference{Kind: "none", Reason: noneReason(app, flow, history), History: history}
}

// noneReason flattens the history for the surfaces that render one string.
// It names every candidate rather than the first three: a flow with six
// dirty runs whose reason stops at three reads as if the other three were
// never examined, which is the same "present but silent" failure History
// exists to prevent.
func noneReason(app, flow string, history []Candidate) string {
	parts := make([]string, 0, len(history))
	for _, c := range history {
		parts = append(parts, c.RunID+": "+c.Reason)
	}
	return fmt.Sprintf("no run eligible as a reference for %s/%s — %s", app, flow, strings.Join(parts, "; "))
}

// --- accept -------------------------------------------------------------

// AcceptOptions is one promotion: which run, in which project, becomes the
// reference for its flow.
type AcceptOptions struct {
	Cwd, RunsRoot, App, Flow, RunID string
	// MasksFor is the ONLY mask input, and it is a function because masks
	// are per-checkpoint by nature — the whole point is "ignore the clock in
	// the header of THIS screen". A flat []pixel.Rect alongside it would be
	// a precedence question with no answer, resolved differently by any two
	// readers.
	//
	// It is already []pixel.Rect: the config.Rect -> pixel.Rect conversion
	// happens ONCE, in the caller's closure, through pixel.RectsFrom. This
	// package never imports retrace/config.
	//
	// nil means "no masks anywhere", which is the honest reading of a
	// caller that has no config — not a trap, because an unmasked shot is
	// the captured truth, and Accept's job is to not LEAK past a mask that
	// exists, never to invent one.
	MasksFor func(checkpoint string) []pixel.Rect
}

// AcceptResult is what a promotion did, in the shape `--json` emits.
type AcceptResult struct {
	Dir   string   `json:"dir"`
	Files []string `json:"files"` // bundle-relative, slash-separated
	RunID string   `json:"runId"`
	Bytes int64    `json:"bytes"`
	// CaptureStatus is the promoted run's own capture verdict, carried
	// through as a TYPED value rather than left reconstructible from
	// warning text. A promotion off a non-ok capture is the human's call to
	// make, but the machine-readable record of having made it belongs here.
	CaptureStatus trace.Verdict `json:"captureStatus"`
}

// bundleFile is one file staged for a bundle: where it came from, where it
// goes, and (for a masked shot) the re-encoded bytes that replace it.
type bundleFile struct {
	rel   string // bundle-relative, slash-separated
	src   string
	bytes []byte // non-nil when the content was rewritten (a redacted shot)
	size  int64
}

// Accept promotes one run into the committed reference bundle for its flow.
//
// It carries manifest.json, wire.jsonl, hops.jsonl and the checkpoint shots
// — and nothing else. misses.jsonl (a replay artifact), groups.jsonl (raw
// marker records, already folded into Manifest.Groups) and any logs are not
// reference material: a bundle is committed, so every byte in it is a cost
// every clone pays forever.
//
// Only the shots the MANIFEST names are carried, never a directory listing
// of shots/. That is what guarantees every promoted image went through
// MasksFor: a stray file in shots/ has no checkpoint name, so there is no
// mask to look up for it, and copying it would be the unredacted promotion
// this function exists to prevent.
//
// The bundle is STAGED in a sibling directory and moved into place, rather
// than the RemoveAll-then-copy the plan sketched. A refusal partway through
// a copy — an undecodable shot, an over-budget file — would otherwise have
// already destroyed the previous reference, turning "I refused to promote
// this" into "you now have no reference at all".
func Accept(o AcceptOptions) (AcceptResult, error) {
	p, err := runs.PathsFor(o.RunsRoot, o.App, o.Flow, o.RunID)
	if err != nil {
		return AcceptResult{}, err
	}
	dir, err := BundleDir(o.Cwd, o.App, o.Flow)
	if err != nil {
		return AcceptResult{}, err
	}
	m, err := runs.ReadManifest(p.ManifestPath)
	if err != nil {
		return AcceptResult{}, fmt.Errorf("reading the manifest for %s/%s/%s: %w", o.App, o.Flow, o.RunID, err)
	}

	files := []bundleFile{{rel: "manifest.json", src: p.ManifestPath}}
	for _, name := range []string{"wire.jsonl", "hops.jsonl"} {
		src := filepath.Join(p.RunDir, name)
		if _, err := os.Stat(src); err == nil {
			files = append(files, bundleFile{rel: name, src: src})
		} else if !errors.Is(err, fs.ErrNotExist) {
			return AcceptResult{}, err
		}
	}
	for _, cp := range m.Checkpoints {
		f, err := shotFor(p.RunDir, cp, o.MasksFor)
		if err != nil {
			return AcceptResult{}, err
		}
		files = append(files, f)
	}
	total, err := measure(files)
	if err != nil {
		return AcceptResult{}, err
	}
	if total > MaxBundleBytes {
		return AcceptResult{}, overBudget(o.App, o.Flow, total, files)
	}

	if err := stageAndSwap(dir, files); err != nil {
		return AcceptResult{}, err
	}
	names := make([]string, len(files))
	for i, f := range files {
		names[i] = f.rel
	}
	return AcceptResult{Dir: dir, Files: names, RunID: o.RunID, Bytes: total, CaptureStatus: m.Capture.Status}, nil
}

// shotFor stages one checkpoint's screenshot, redacting it when a mask
// covers it. Masks previously only gated COMPARISON, which meant a blessed
// shot could reach a committed reference bundle with legible card data
// still in it. Accept is the only place that can be fixed, because it is
// the only place the bytes are copied.
//
// An undecodable shot is a REFUSAL, never a byte-for-byte copy: a mask that
// cannot be applied is a mask that is not protecting anything, and the
// whole point of this branch is what the mask hides.
func shotFor(runDir string, cp runs.Checkpoint, masksFor func(string) []pixel.Rect) (bundleFile, error) {
	// cp.File is run-dir-relative and comes from the manifest; it is
	// resolved against runDir and must not escape it. filepath.Join cleans
	// "..", so the check is on the cleaned result, not the input.
	src := filepath.Join(runDir, filepath.FromSlash(cp.File))
	if rel, err := filepath.Rel(runDir, src); err != nil || strings.HasPrefix(rel, "..") {
		return bundleFile{}, fmt.Errorf("checkpoint %q names %q, which is outside the run directory", cp.Name, cp.File)
	}
	rel := filepath.ToSlash(filepath.Clean(cp.File))
	var masks []pixel.Rect
	if masksFor != nil {
		masks = masksFor(cp.Name)
	}
	if len(masks) == 0 {
		return bundleFile{rel: rel, src: src}, nil
	}
	raw, err := os.ReadFile(src)
	if err != nil {
		return bundleFile{}, fmt.Errorf("reading checkpoint %q for redaction: %w", cp.Name, err)
	}
	img, err := pixel.Decode(raw)
	if err != nil {
		return bundleFile{}, fmt.Errorf("checkpoint %q is masked but could not be decoded, so its mask cannot be applied: %w — refusing to promote it unredacted", cp.Name, err)
	}
	pixel.ApplyMasks(img, masks)
	out, err := pixel.Encode(img)
	if err != nil {
		return bundleFile{}, fmt.Errorf("re-encoding the redacted checkpoint %q: %w", cp.Name, err)
	}
	return bundleFile{rel: rel, src: src, bytes: out, size: int64(len(out))}, nil
}

// measure fills in each staged file's size — the REDACTED size for a
// rewritten shot, because that is the byte count the bundle actually costs.
func measure(files []bundleFile) (int64, error) {
	var total int64
	for i := range files {
		if files[i].bytes == nil {
			st, err := os.Stat(files[i].src)
			if err != nil {
				return 0, err
			}
			files[i].size = st.Size()
		}
		total += files[i].size
	}
	return total, nil
}

func overBudget(app, flow string, total int64, files []bundleFile) error {
	largest := files[0]
	for _, f := range files[1:] {
		if f.size > largest.size {
			largest = f
		}
	}
	return fmt.Errorf("reference bundle for %s/%s would be %s, over the %s budget — the largest file is %s (%s); add a mask, trim the flow, or raise MaxBundleBytes deliberately",
		app, flow, humanBytes(total), humanBytes(MaxBundleBytes), largest.rel, humanBytes(largest.size))
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// stageAndSwap writes every staged file into a sibling temp directory, then
// replaces dir with it. REPLACES: a screen deleted from the flow must not
// linger in the reference, and neither must a hops.jsonl from a run that
// recorded one when this one did not.
func stageAndSwap(dir string, files []bundleFile) error {
	parent := filepath.Dir(dir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(parent, ".staging-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging) // no-op once the rename has moved it away
	for _, f := range files {
		if err := writeBundleFile(filepath.Join(staging, filepath.FromSlash(f.rel)), f); err != nil {
			return err
		}
	}
	// A same-parent rename cannot span filesystems, so the only window is
	// between the RemoveAll and the Rename — orders of magnitude smaller
	// than a whole copy, and a crash inside it leaves no half-bundle,
	// because the staged tree is complete before either call runs.
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	return os.Rename(staging, dir)
}

func writeBundleFile(dst string, f bundleFile) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if f.bytes != nil {
		return os.WriteFile(dst, f.bytes, 0o644)
	}
	b, err := os.ReadFile(f.src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}

// --- reject -------------------------------------------------------------

// RejectOptions captures a failing run as an attachable repro bundle.
// OutDir defaults to <Cwd>/.retrace/repro. Summary may be nil — a run can
// be rejected before any reference exists to diff it against.
type RejectOptions struct {
	Cwd, RunsRoot, App, Flow, RunID, OutDir string
	Summary                                 *diff.Summary
}

type RejectResult struct {
	Dir   string   `json:"dir"`
	Files []string `json:"files"`
}

// Reject copies a failing run — manifest, both hop planes, its shots, its
// replay misses — plus the diff.Summary that motivated the rejection, into
// <OutDir>/<app>__<flow>__<runId>/: something a human can attach to a bug.
//
// It carries MORE than Accept, not less, and deliberately so. The two
// bundles have opposite purposes: a reference is committed and must be
// minimal, while a repro is thrown away after someone reads it and wants
// everything — misses.jsonl especially, which is exactly what a replay
// failure is about.
//
// Shots are copied VERBATIM, never redacted: a repro bundle is evidence,
// and evidence with the interesting region painted black is not evidence.
// It is also not committed — .retrace/repro/ is ignored (see .gitignore) —
// so the leak Accept's redaction prevents does not arise here.
//
// A nil Summary writes no summary.json at all. An empty one would assert a
// comparison that never ran, which is the "a plausible value is worse than
// an absent one" trap in file form.
func Reject(o RejectOptions) (RejectResult, error) {
	p, err := runs.PathsFor(o.RunsRoot, o.App, o.Flow, o.RunID)
	if err != nil {
		return RejectResult{}, err
	}
	// Validated again, deliberately: PathsFor guarded the RUN-side join, and
	// the line below builds a SECOND path out of the same three components.
	// A function that joins a caller-supplied component into a filesystem
	// path validates that component at the seam; relying on PathsFor having
	// been called first would make this guard a property of statement order.
	if err := runs.ValidateComponents(o.App, o.Flow, o.RunID); err != nil {
		return RejectResult{}, err
	}
	outDir := o.OutDir
	if outDir == "" {
		outDir = filepath.Join(o.Cwd, ".retrace", "repro")
	}
	dir := filepath.Join(outDir, o.App+"__"+o.Flow+"__"+o.RunID)
	if err := os.RemoveAll(dir); err != nil {
		return RejectResult{}, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return RejectResult{}, err
	}

	var files []string
	for _, name := range []string{"manifest.json", "wire.jsonl", "hops.jsonl", "misses.jsonl", "groups.jsonl"} {
		src := filepath.Join(p.RunDir, name)
		b, err := os.ReadFile(src)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return RejectResult{}, err
		}
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			return RejectResult{}, err
		}
		files = append(files, name)
	}
	shots, err := os.ReadDir(p.ShotsDir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return RejectResult{}, err
	}
	for _, e := range shots {
		if e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(p.ShotsDir, e.Name()))
		if err != nil {
			return RejectResult{}, err
		}
		if err := os.MkdirAll(filepath.Join(dir, "shots"), 0o755); err != nil {
			return RejectResult{}, err
		}
		if err := os.WriteFile(filepath.Join(dir, "shots", e.Name()), b, 0o644); err != nil {
			return RejectResult{}, err
		}
		files = append(files, "shots/"+e.Name())
	}
	if o.Summary != nil {
		b, err := json.MarshalIndent(o.Summary, "", "  ")
		if err != nil {
			return RejectResult{}, err
		}
		if err := os.WriteFile(filepath.Join(dir, "summary.json"), append(b, '\n'), 0o644); err != nil {
			return RejectResult{}, err
		}
		files = append(files, "summary.json")
	}
	return RejectResult{Dir: dir, Files: files}, nil
}
