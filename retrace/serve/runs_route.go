package serve

import (
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/caribou-crew/ensemble/retrace/diff"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

// RunRow is one run of a surface (app/flow) in the runs-list — the drill-down
// behind a queue row. It is deliberately lighter than Item: Item is "the one
// run worth reviewing for this surface" (always the newest), a RunRow is
// "one of the surface's runs, enough to pick which to open".
type RunRow struct {
	RunID string `json:"runId"`
	// Verdict is the same four-value vocabulary as Item.Verdict / Summary.Verdict.
	Verdict string `json:"verdict"`
	// When the run finished (Manifest.FinishedAt, falling back to StartedAt),
	// so the list can sort and show recency. Zero time if the manifest had
	// neither — the UI then falls back to parsing the runId's own timestamp
	// stamp.
	When time.Time `json:"when"`
	// Source is nil for a locally recorded run, set for a CI sync — the same
	// presence contract as Item.Source.
	Source *runs.Source `json:"source,omitempty"`
	Counts diff.Counts  `json:"counts"`
	// Gates is never nil (see Item.Gates) — the reasons this run is flagged.
	Gates []string `json:"gates"`
}

func (s *server) handleRuns(w http.ResponseWriter, r *http.Request) {
	d, app, flow, ok := s.flowFrom(w, r)
	if !ok {
		return
	}
	WriteRuns(w, d, app, flow)
}

// WriteRuns writes every run of one surface, newest first. Exported for the
// same reason WriteQueue / WriteItem are: ensemble/server's retrace routes
// can serve the identical body without a second definition of the shape.
func WriteRuns(w http.ResponseWriter, d Deps, app, flow string) {
	rows, err := RunsFor(d, app, flow)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// rows is never nil — an empty surface encodes as [] not null, the same
	// contract writeQueueItems relies on.
	writeJSON(w, http.StatusOK, map[string]any{"runs": rows})
}

// RunsFor lists every run recorded for app/flow, newest first, each carrying
// the verdict its own comparison produced (against the surface's reference),
// its provenance, and its counts. It computes a full SummaryForRun per run,
// which is the same work opening the run at the detail view does — fine for
// a surface with a handful of runs; if a project's run history grows into
// the hundreds this is the place to switch to a cheap manifest-only listing
// with lazy verdicts.
func RunsFor(d Deps, app, flow string) ([]RunRow, error) {
	if err := d.check(); err != nil {
		return nil, err
	}
	if err := runs.ValidateComponents(app, flow); err != nil {
		return nil, err
	}
	root := runs.RunsRoot(d.Cwd)
	ids, err := runs.ListRunsErr(root, app, flow)
	if err != nil {
		return nil, err
	}
	// Each run's summary is independent, so compute them concurrently with a
	// bounded pool: a surface with dozens of CI runs otherwise pays the sum
	// of every per-run diff serially, when the work parallelises to roughly
	// the slowest single run. The cap keeps a huge history from spawning
	// hundreds of goroutines that thrash the disk cache.
	rows := make([]RunRow, len(ids))
	const workers = 8
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, id string) {
			defer wg.Done()
			defer func() { <-sem }()
			row := RunRow{RunID: id, Gates: []string{}}
			// A run that cannot be summarised (unreadable manifest, missing
			// shots) is not dropped — it is reported as "quarantined" with
			// the error as its gate, so a broken run is visible in the list
			// rather than silently absent, the same fail-visible stance
			// BuildQueue takes.
			sum, sErr := SummaryForRun(d, app, flow, id)
			if sErr != nil {
				row.Verdict = "quarantined"
				row.Gates = []string{sErr.Error()}
				if p, pErr := runs.PathsFor(root, app, flow, id); pErr == nil {
					row.Source = sourceOf(p.RunDir)
				}
			} else {
				row.Verdict = sum.Verdict
				row.Counts = sum.Counts
				row.Gates = itemGates(sum)
				row.When = whenOf(sum.B.Manifest)
				row.Source = sourceOf(sum.B.Dir)
			}
			rows[i] = row
		}(i, id)
	}
	wg.Wait()
	// Newest first: the run a reviewer most likely wants is the latest, and
	// it is also the one the queue row already summarised. Ties (equal or
	// zero timestamps) fall back to the runId string, which begins with the
	// timestamp stamp, so the order is still stable and chronological.
	sort.SliceStable(rows, func(i, j int) bool {
		if !rows[i].When.Equal(rows[j].When) {
			return rows[i].When.After(rows[j].When)
		}
		return rows[i].RunID > rows[j].RunID
	})
	return rows, nil
}

// whenOf is a run's finish time, preferring FinishedAt and falling back to
// StartedAt — either is a truer "when did this run happen" than the runId
// stamp, which the UI only parses when both are zero.
func whenOf(m runs.Manifest) time.Time {
	if !m.FinishedAt.IsZero() {
		return m.FinishedAt
	}
	return m.StartedAt
}
