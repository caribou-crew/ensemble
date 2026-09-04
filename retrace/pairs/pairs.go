// Package pairs owns the on-disk shape of a persisted cross-app diff: a
// `retrace diff -a <appA>@<selA> -b <appB>@<selB>` comparison between two
// DIFFERENT apps, written alongside a diff/overlay image set exactly the
// way a same-app `retrace diff` already writes into side B's own run
// directory (retrace/cmd/retrace/cmd_diff.go). It exists so `retrace serve`
// can discover and render a cross-app comparison the CLI already computed,
// without recomputing it — see
// docs/superpowers/specs/2026-09-04-cross-app-compare-view-design.md.
//
// There is no index file: List walks the runs tree the same way
// runs.ListApps/ListFlows/ListRuns already do, matching every other
// discovery mechanism in retrace.
package pairs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"github.com/caribou-crew/ensemble/retrace/diff"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

// Schema versions the pairing manifest (pair.json), independently of
// diff.SummarySchema — this document gains fields on its own schedule.
const Schema = "retrace/pair/1"

// Pair is pair.json's shape: enough to build a listing row and a link to
// the full comparison without decoding every summary.json on disk.
type Pair struct {
	Schema     string      `json:"schema"`
	AppA       string      `json:"appA"`
	FlowA      string      `json:"flowA"`
	RunA       string      `json:"runA"`
	AppB       string      `json:"appB"`
	FlowB      string      `json:"flowB"`
	RunB       string      `json:"runB"`
	PairID     string      `json:"pairId"`
	ComputedAt time.Time   `json:"computedAt"`
	Verdict    string      `json:"verdict"`
	Counts     diff.Counts `json:"counts"`
	// Dir is where this pairing was found — filled in by List/Read/Persist
	// from the directory pair.json lives in, and deliberately not written
	// into pair.json itself: nothing on disk needs to record its own
	// location, the same reasoning refs.Reference.Dir is set by Resolve
	// rather than stored in a bundle's own manifest.json.
	Dir string `json:"-"`
}

// unsafeRune is every rune runs.validComponent's charset excludes — a
// pairId is built from an app name and a run id/"reference" and must be a
// single, directory-safe path component.
var unsafeRune = regexp.MustCompile(`[^A-Za-z0-9._-]`)

func idComponent(s string) string {
	return unsafeRune.ReplaceAllString(s, "_")
}

// RunIdentity is the directory-safe identity one side of a comparison
// contributes to a pairing directory name: the literal "reference" for a
// resolved bundle, or the run id otherwise.
//
// The literal token, not the run id CURRENTLY backing the bundle: `-a
// reference` always names the same pairing directory regardless of which
// run is promoted into the bundle between two invocations — the same
// reason runs.RefRunID is a fixed string rather than the source run's id.
func RunIdentity(ref diff.RunRef) string {
	if ref.Kind == "bundle" {
		return runs.RefRunID
	}
	return ref.RunID
}

// ID names one A-side as a single directory-safe path segment:
// "<appA>__<runIdentity>" — the same "__" separator refs.Reject already
// uses to join app/flow/runId into one path component.
func ID(appA string, a diff.RunRef) string {
	return idComponent(appA) + "__" + idComponent(RunIdentity(a))
}

// DirFor is where a cross-app diff between a and B is persisted, given
// bDir — B's own already-resolved directory (a run directory, or, when B
// itself resolved to "reference", the reference bundle directory). A
// sibling of bDir named for the A side, so one B run can accumulate diffs
// against several different A sides without collision.
func DirFor(bDir string, a diff.RunRef) string {
	return filepath.Join(bDir, "diffs", ID(a.Manifest.App, a))
}

// Persist writes pair.json and summary.json into dir — the CLI face of a
// cross-app `retrace diff`. dir is what DirFor returned and is also the
// OutDir the caller passed to diff.Build, so the diff/overlay PNGs Build
// already wrote (diff.writeCheckpointImages) sit alongside these two files
// with no separate copy step.
func Persist(dir string, s diff.Summary, now time.Time) (Pair, error) {
	p := Pair{
		Schema: Schema,
		AppA:   s.A.Manifest.App, FlowA: s.Flow, RunA: RunIdentity(s.A),
		AppB: s.B.Manifest.App, FlowB: s.Flow, RunB: RunIdentity(s.B),
		PairID: filepath.Base(dir), ComputedAt: now.UTC(), Verdict: s.Verdict,
		Counts: s.Counts,
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Pair{}, fmt.Errorf("pairs: persisting to %s: %w", dir, err)
	}
	if err := writeJSONFile(filepath.Join(dir, "pair.json"), p); err != nil {
		return Pair{}, err
	}
	if err := writeJSONFile(filepath.Join(dir, "summary.json"), s); err != nil {
		return Pair{}, err
	}
	p.Dir = dir
	return p, nil
}

func writeJSONFile(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("pairs: encoding %s: %w", path, err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		return fmt.Errorf("pairs: writing %s: %w", path, err)
	}
	return nil
}

// Read reads one persisted pairing's pair.json from dir.
func Read(dir string) (Pair, error) {
	b, err := os.ReadFile(filepath.Join(dir, "pair.json"))
	if err != nil {
		return Pair{}, err
	}
	var p Pair
	if err := json.Unmarshal(b, &p); err != nil {
		return Pair{}, fmt.Errorf("pairs: %s: %w", dir, err)
	}
	p.Dir = dir
	return p, nil
}

// ReadSummary reads one persisted pairing's full summary.json from dir.
func ReadSummary(dir string) (diff.Summary, error) {
	b, err := os.ReadFile(filepath.Join(dir, "summary.json"))
	if err != nil {
		return diff.Summary{}, err
	}
	var s diff.Summary
	if err := json.Unmarshal(b, &s); err != nil {
		return diff.Summary{}, fmt.Errorf("pairs: %s: %w", dir, err)
	}
	return s, nil
}

// List walks runsRoot for every persisted cross-app diff —
// <app>/<flow>/<runId>/diffs/<pairId>/pair.json — newest first. There is no
// index file; this is the same "discover by walking directories"
// convention runs.ListApps/ListFlows/ListRuns already use.
//
// A run with no diffs/ subfolder (the overwhelming common case) is skipped
// with no error. A malformed pair.json is skipped too, not fatal: one
// corrupt directory must not take down the whole listing.
func List(runsRoot string) ([]Pair, error) {
	apps, err := runs.ListAppsErr(runsRoot)
	if err != nil {
		return nil, fmt.Errorf("pairs: cannot read the runs root %s: %w", runsRoot, err)
	}
	out := []Pair{}
	for _, app := range apps {
		for _, flow := range runs.ListFlows(runsRoot, app) {
			for _, runID := range runs.ListRuns(runsRoot, app, flow) {
				p, err := runs.PathsFor(runsRoot, app, flow, runID)
				if err != nil {
					continue
				}
				entries, err := os.ReadDir(filepath.Join(p.RunDir, "diffs"))
				if err != nil {
					continue
				}
				for _, e := range entries {
					if !e.IsDir() {
						continue
					}
					pr, err := Read(filepath.Join(p.RunDir, "diffs", e.Name()))
					if err != nil {
						continue
					}
					out = append(out, pr)
				}
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ComputedAt.After(out[j].ComputedAt) })
	return out, nil
}
