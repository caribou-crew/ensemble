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

// candidateFor decides one run's eligibility, in the same order the prototype's
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
	if m, err := runs.ReadManifest(filepath.Join(dir, "manifest.json")); err == nil {
		// RunID keeps the PROVENANCE of the run the bundle was promoted
		// from; the directory is the literal "reference".
		return Reference{Kind: "bundle", Dir: dir, RunID: m.RunID, Manifest: m}
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
