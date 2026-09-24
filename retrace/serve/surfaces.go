package serve

import (
	"net/http"
	"sort"
	"time"

	"github.com/caribou-crew/ensemble/retrace/refs"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

// SurfaceRun is one run as the manifest records it — no diff is computed,
// so listing every surface's history stays cheap.
type SurfaceRun struct {
	RunID  string       `json:"runId"`
	When   time.Time    `json:"when"`
	Source *runs.Source `json:"source,omitempty"`
	// Capture is the manifest's own trust verdict — the report picker needs
	// it to skip a newer run that failed capture in favor of an older usable
	// one, without diffing every candidate first.
	Capture runs.CaptureTrust `json:"capture"`
	// Checkpoints is len(manifest.Checkpoints). Zero here fails closed the
	// same way an empty Wire/Hops count does: a run with no checkpoints was
	// not usefully captured, whatever Capture.Status says.
	Checkpoints int `json:"checkpoints"`
}

// Baseline is what a diff against the accepted reference would resolve to
// for this app/flow, without actually diffing — the dashboard uses it to
// decide whether asking for that comparison is even meaningful, rather than
// firing the request and reading a 409 back.
type Baseline struct {
	// Kind mirrors refs.Reference.Kind: "bundle" | "run" | "none". "none"
	// means no comparison is possible yet — nothing has been accepted, and
	// no eligible run exists to fall back to.
	Kind  string `json:"kind"`
	RunID string `json:"runId,omitempty"`
}

// Surface is one app/flow and its runs, newest first.
type Surface struct {
	App      string       `json:"app"`
	Flow     string       `json:"flow"`
	Runs     []SurfaceRun `json:"runs"`
	Baseline Baseline     `json:"baseline"`
}

// ListSurfaces enumerates the same app/flows BuildQueue does, without diffing.
func ListSurfaces(d Deps) ([]Surface, error) {
	if err := d.check(); err != nil {
		return nil, err
	}
	root := runs.RunsRoot(d.Cwd)
	apps, err := runs.ListAppsErr(root)
	if err != nil {
		return nil, err
	}
	out := []Surface{}
	for _, app := range apps {
		flows, ferr := runs.ListFlowsErr(root, app)
		if ferr != nil || !appIsReal(d, root, app, flows) {
			continue
		}
		for _, flow := range flows {
			ids, lerr := runs.ListRunsErr(root, app, flow)
			if lerr != nil {
				continue
			}
			ref := refs.Resolve(d.Cwd, root, app, flow)
			s := Surface{App: app, Flow: flow, Runs: make([]SurfaceRun, 0, len(ids)), Baseline: Baseline{Kind: ref.Kind, RunID: ref.RunID}}
			for _, id := range ids {
				p, perr := runs.PathsFor(root, app, flow, id)
				if perr != nil {
					continue
				}
				row := SurfaceRun{RunID: id, Source: sourceOf(p.RunDir)}
				if m, merr := runs.ReadManifest(p.ManifestPath); merr == nil {
					row.When = whenOf(m)
					row.Capture = m.Capture
					row.Checkpoints = len(m.Checkpoints)
				}
				s.Runs = append(s.Runs, row)
			}
			// Run ids lead with a UTC timestamp, so descending id is newest first.
			sort.Slice(s.Runs, func(i, j int) bool { return s.Runs[i].RunID > s.Runs[j].RunID })
			out = append(out, s)
		}
	}
	return out, nil
}

// ListSurfaces aggregates ListSurfaces across every root.
func (s Sources) ListSurfaces() ([]Surface, error) {
	out := []Surface{}
	for _, d := range s.Roots() {
		rows, err := ListSurfaces(d)
		if err != nil {
			return nil, err
		}
		out = append(out, rows...)
	}
	return out, nil
}

func (s *server) handleSurfaces(w http.ResponseWriter, _ *http.Request) {
	var (
		rows []Surface
		err  error
	)
	if sources := s.currentSources(); sources != nil {
		rows, err = sources.ListSurfaces()
	} else {
		rows, err = ListSurfaces(s.deps())
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"surfaces": rows})
}
