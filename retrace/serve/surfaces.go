package serve

import (
	"net/http"
	"sort"
	"time"

	"github.com/caribou-crew/ensemble/retrace/runs"
)

// SurfaceRun is one run as the manifest records it — no diff is computed,
// so listing every surface's history stays cheap.
type SurfaceRun struct {
	RunID  string       `json:"runId"`
	When   time.Time    `json:"when"`
	Source *runs.Source `json:"source,omitempty"`
}

// Surface is one app/flow and its runs, newest first.
type Surface struct {
	App  string       `json:"app"`
	Flow string       `json:"flow"`
	Runs []SurfaceRun `json:"runs"`
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
			s := Surface{App: app, Flow: flow, Runs: make([]SurfaceRun, 0, len(ids))}
			for _, id := range ids {
				p, perr := runs.PathsFor(root, app, flow, id)
				if perr != nil {
					continue
				}
				row := SurfaceRun{RunID: id, Source: sourceOf(p.RunDir)}
				if m, merr := runs.ReadManifest(p.ManifestPath); merr == nil {
					row.When = whenOf(m)
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
