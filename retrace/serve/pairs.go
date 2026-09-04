package serve

import (
	"fmt"
	"path/filepath"
	"sort"
	"time"

	"github.com/caribou-crew/ensemble/retrace/diff"
	"github.com/caribou-crew/ensemble/retrace/pairs"
	"github.com/caribou-crew/ensemble/retrace/refs"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

// PairItem is one persisted cross-app diff's line in the pairs listing —
// pairs.Pair plus a wire-friendly ComputedAt, mirroring Item's own
// relationship to diff.Summary.
type PairItem struct {
	AppA       string      `json:"appA"`
	FlowA      string      `json:"flowA"`
	RunA       string      `json:"runA"`
	AppB       string      `json:"appB"`
	FlowB      string      `json:"flowB"`
	RunB       string      `json:"runB"`
	PairID     string      `json:"pairId"`
	ComputedAt string      `json:"computedAt"`
	Verdict    string      `json:"verdict"`
	Counts     diff.Counts `json:"counts"`
}

func pairItemOf(p pairs.Pair) PairItem {
	return PairItem{
		AppA: p.AppA, FlowA: p.FlowA, RunA: p.RunA,
		AppB: p.AppB, FlowB: p.FlowB, RunB: p.RunB,
		PairID: p.PairID, ComputedAt: p.ComputedAt.Format(time.RFC3339),
		Verdict: p.Verdict, Counts: p.Counts,
	}
}

// ListPairs lists every persisted cross-app diff found under d's project
// root — both run-rooted and reference-bundle-rooted pairings (see
// pairs.List) — newest first.
func ListPairs(d Deps) ([]PairItem, error) {
	if err := d.check(); err != nil {
		return nil, err
	}
	ps, err := pairs.List(d.Cwd)
	if err != nil {
		return nil, err
	}
	out := make([]PairItem, 0, len(ps))
	for _, p := range ps {
		out = append(out, pairItemOf(p))
	}
	return out, nil
}

// ListPairs aggregates ListPairs across every root in a Sources — the
// pairs.go counterpart to Sources.BuildQueue.
func (s Sources) ListPairs() ([]PairItem, error) {
	var items []PairItem
	for _, d := range s.Roots() {
		rootItems, err := ListPairs(d)
		if err != nil {
			return nil, fmt.Errorf("serve: listing cross-app diffs for %s: %w", d.Cwd, err)
		}
		items = append(items, rootItems...)
	}
	if items == nil {
		items = []PairItem{}
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].ComputedAt > items[j].ComputedAt })
	return items, nil
}

// sideDirFor resolves app/flow/run to the directory holding that side's own
// captured shots — a run directory, or (run == runs.RefRunID) the app's
// committed reference bundle. Shared by pairDirFor (B) and handlePairShot's
// A-side resolution, so neither ever serves a filesystem path read out of a
// persisted data file — only app/flow/run values re-validated the same way
// every other route validates a request-supplied path component.
func sideDirFor(d Deps, app, flow, run string) (string, error) {
	if run == runs.RefRunID {
		return refs.BundleDir(d.Cwd, app, flow)
	}
	p, err := runs.PathsFor(runs.RunsRoot(d.Cwd), app, flow, run)
	if err != nil {
		return "", err
	}
	return p.RunDir, nil
}

// pairDirFor resolves {appB}/{flowB}/{runB}/{pairId} to the directory a
// persisted cross-app diff lives in. runB is either a real run id or the
// literal "reference" (runs.RefRunID) — B can resolve to a reference bundle
// the same way A can (see pairs.RunIdentity).
//
// Every component is validated through runs.ValidateComponents before any
// join — the same guard PathsFor's own doc comment requires at every seam
// that joins a request-supplied value into a filesystem path.
func pairDirFor(d Deps, appB, flowB, runB, pairID string) (string, error) {
	if err := runs.ValidateComponents(appB, flowB, runB, pairID); err != nil {
		return "", err
	}
	bDir, err := sideDirFor(d, appB, flowB, runB)
	if err != nil {
		return "", err
	}
	return filepath.Join(bDir, "diffs", pairID), nil
}
