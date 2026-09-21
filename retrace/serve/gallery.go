package serve

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"time"

	"github.com/caribou-crew/ensemble/retrace/diff"
	"github.com/caribou-crew/ensemble/retrace/pairs"
	"github.com/caribou-crew/ensemble/retrace/suites"
)

// Gallery is the whole-build review board: one row per flow, one tile per
// platform lane, so a reviewer sees web, iOS and Android together. It carries
// references, never pixels; clients compose image URLs from the same identifiers
// the pair and screen routes already use.
type Gallery struct {
	SuiteID string `json:"suiteId"`
	Title   string `json:"title"`
	// Scope is "build" (one source revision) or "latest" (each lane's newest
	// result across revisions, every tile labelled with its own revision).
	Scope      string        `json:"scope"`
	BuildID    string        `json:"buildId"`
	Git        suites.Git    `json:"git"`
	BaselineID string        `json:"baselineId"`
	PolicyID   string        `json:"policyId"`
	UpdatedAt  string        `json:"updatedAt"`
	Platforms  []string      `json:"platforms"`
	Lanes      []GalleryLane `json:"lanes"`
	Rows       []GalleryRow  `json:"rows"`
}

// GallerySource names the revision a tile's result was produced on. In the
// "latest" scope tiles in one row can come from different revisions, so the
// label is part of the evidence, not decoration.
type GallerySource struct {
	BuildID    string `json:"buildId"`
	SHA        string `json:"sha"`
	Branch     string `json:"branch"`
	Dirty      bool   `json:"dirty"`
	BaselineID string `json:"baselineId"`
	PolicyID   string `json:"policyId"`
	FinishedAt string `json:"finishedAt"`
}

// GalleryLane lists the distinct revisions that contribute to a platform column.
type GalleryLane struct {
	Platform string          `json:"platform"`
	Sources  []GallerySource `json:"sources"`
}
type GalleryLabel struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}
type GalleryRow struct {
	Feature GalleryLabel  `json:"feature"`
	Flow    GalleryLabel  `json:"flow"`
	Tiles   []GalleryTile `json:"tiles"`
}
type GalleryTile struct {
	Platform  string           `json:"platform"`
	Status    string           `json:"status"`
	Planes    suites.Planes    `json:"planes"`
	Reason    string           `json:"reason,omitempty"`
	AttemptID string           `json:"attemptId,omitempty"`
	Evidence  *suites.Evidence `json:"evidence,omitempty"`
	Screens   []GalleryScreen  `json:"screens"`
	Pair      *GalleryPair     `json:"pair,omitempty"`
	Wire      GalleryWire      `json:"wire"`
	Source    *GallerySource   `json:"source,omitempty"`
}
type GalleryScreen struct {
	Label  string `json:"label"`
	SHA256 string `json:"sha256"`
	Media  string `json:"media"`
}

// GalleryPair points at a saved reference/candidate comparison. Available=false
// means the report links a pair the store cannot read; that is reported, never
// silently shown as an unchanged or absent comparison.
type GalleryPair struct {
	App         string              `json:"app"`
	Flow        string              `json:"flow"`
	RunID       string              `json:"runId"`
	PairID      string              `json:"pairId"`
	Available   bool                `json:"available"`
	Error       string              `json:"error,omitempty"`
	Checkpoint  string              `json:"checkpoint,omitempty"`
	Verdict     string              `json:"verdict,omitempty"`
	Checkpoints []GalleryCheckpoint `json:"checkpoints,omitempty"`
}
type GalleryCheckpoint struct {
	Name    string  `json:"name"`
	Verdict string  `json:"verdict"`
	DiffPct float64 `json:"diffPct"`
}

// GalleryWire is the explicit wire callout. Represented is true only when a
// readable saved comparison supplied Counts. Otherwise it is false, Plane is the
// runner's asserted wire state (never a verdict this server computed) and Note
// says why no wire diff is shown.
type GalleryWire struct {
	Represented bool        `json:"represented"`
	Plane       string      `json:"plane"`
	Note        string      `json:"note"`
	Counts      *WireCounts `json:"counts,omitempty"`
}
type WireCounts struct {
	Paired     int `json:"paired"`
	Changed    int `json:"changed"`
	Moved      int `json:"moved"`
	Missing    int `json:"missing"`
	Extra      int `json:"extra"`
	Violations int `json:"violations"`
}

const (
	noResultNote = "No result has been imported for this lane, so there is no wire evidence."
	noPairNote   = "Wire diff not represented here: no saved comparison is linked to this result."
	badPairNote  = "Wire diff not represented here: the linked comparison could not be read."
)

func (s *server) handleGallery(w http.ResponseWriter, r *http.Request) {
	suiteID, buildID := r.PathValue("suite"), r.PathValue("build")
	items, err := suites.Load(s.deps().Cwd)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	g, err := BuildGallery(items, suiteID, buildID, s.pairReader())
	if errors.Is(err, errNoBuild) {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, g)
}

func (s *server) handleLatestGallery(w http.ResponseWriter, r *http.Request) {
	items, err := suites.Load(s.deps().Cwd)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	g, err := BuildLatestGallery(items, r.PathValue("suite"), s.pairReader())
	if errors.Is(err, errNoBuild) {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, g)
}

func (s *server) pairReader() func(app, flow, run, pair string) (diff.Summary, error) {
	return func(app, flow, run, pair string) (diff.Summary, error) {
		dir, err := pairDirFor(s.depsForApp(app), app, flow, run, pair)
		if err != nil {
			return diff.Summary{}, err
		}
		return pairs.ReadSummary(dir)
	}
}

var errNoBuild = errors.New("suite build not found")

func gallerySourceOf(b suites.SuiteBuild, finished string) *GallerySource {
	return &GallerySource{BuildID: b.ID, SHA: b.Git.SHA, Branch: b.Git.Branch, Dirty: b.Git.Dirty, BaselineID: b.BaselineID, PolicyID: b.PolicyID, FinishedAt: finished}
}

// newer reports whether a finished later than b; equal times break by attempt id
// so the choice is deterministic (the same rule the aggregate uses per build).
func newer(a, b *suites.AttemptResult) bool {
	if b == nil {
		return a != nil
	}
	if a == nil {
		return false
	}
	ta, ea := time.Parse(time.RFC3339, a.FinishedAt)
	tb, eb := time.Parse(time.RFC3339, b.FinishedAt)
	if ea != nil || eb != nil || ta.Equal(tb) {
		return a.AttemptID > b.AttemptID
	}
	return ta.After(tb)
}

// BuildLatestGallery shows, for every flow and platform, the newest finished
// result across all source revisions. Rows follow the inventory, so a lane with
// no result anywhere is an explicit not-run tile. It is the honest way to put a
// web comparison and a native run of different revisions on one page: each tile
// keeps its own revision label, and nothing is merged into a single build.
func BuildLatestGallery(items []suites.SuiteOverview, suiteID string, readPair func(app, flow, run, pair string) (diff.Summary, error)) (Gallery, error) {
	for _, suite := range items {
		if suite.ID != suiteID {
			continue
		}
		if len(suite.Builds) == 0 {
			return Gallery{}, fmt.Errorf("%w: %s has no imported reports", errNoBuild, suiteID)
		}
		g := Gallery{SuiteID: suite.ID, Title: suite.Title, Scope: "latest", Platforms: suite.Platforms, Rows: []GalleryRow{}, Lanes: []GalleryLane{}}
		type best struct {
			cell  suites.FlowCell
			build suites.SuiteBuild
		}
		sources := map[string]map[string]GallerySource{}
		first := suite.Builds[0]
		for fi, feature := range first.Features {
			for wi, flow := range feature.Flows {
				row := GalleryRow{Feature: GalleryLabel{feature.ID, feature.Title}, Flow: GalleryLabel{flow.ID, flow.Title}, Tiles: []GalleryTile{}}
				for pi, cell := range flow.Platforms {
					pick := best{cell: cell, build: first}
					var chosen *suites.AttemptResult
					for _, b := range suite.Builds {
						c := b.Features[fi].Flows[wi].Platforms[pi]
						if c.Latest != nil && newer(c.Latest, chosen) {
							chosen, pick = c.Latest, best{cell: c, build: b}
						}
					}
					tile := buildTile(pick.cell, readPair)
					if chosen != nil {
						tile.Source = gallerySourceOf(pick.build, chosen.FinishedAt)
						if sources[cell.Platform] == nil {
							sources[cell.Platform] = map[string]GallerySource{}
						}
						sources[cell.Platform][pick.build.ID] = *tile.Source
					}
					row.Tiles = append(row.Tiles, tile)
				}
				g.Rows = append(g.Rows, row)
			}
		}
		for _, p := range suite.Platforms {
			lane := GalleryLane{Platform: p, Sources: []GallerySource{}}
			for _, s := range sources[p] {
				lane.Sources = append(lane.Sources, s)
			}
			sort.Slice(lane.Sources, func(i, j int) bool { return lane.Sources[i].FinishedAt > lane.Sources[j].FinishedAt })
			g.Lanes = append(g.Lanes, lane)
		}
		return g, nil
	}
	return Gallery{}, fmt.Errorf("%w: %s", errNoBuild, suiteID)
}

// BuildGallery assembles the board from the aggregate and a pair reader.
func BuildGallery(items []suites.SuiteOverview, suiteID, buildID string, readPair func(app, flow, run, pair string) (diff.Summary, error)) (Gallery, error) {
	for _, suite := range items {
		if suite.ID != suiteID {
			continue
		}
		for _, build := range suite.Builds {
			if build.ID != buildID {
				continue
			}
			g := Gallery{SuiteID: suite.ID, Title: suite.Title, Scope: "build", BuildID: build.ID, Lanes: []GalleryLane{}, Git: build.Git, BaselineID: build.BaselineID,
				PolicyID: build.PolicyID, UpdatedAt: build.UpdatedAt, Platforms: suite.Platforms, Rows: []GalleryRow{}}
			for _, feature := range build.Features {
				for _, flow := range feature.Flows {
					row := GalleryRow{Feature: GalleryLabel{feature.ID, feature.Title}, Flow: GalleryLabel{flow.ID, flow.Title}, Tiles: []GalleryTile{}}
					for _, cell := range flow.Platforms {
						tile := buildTile(cell, readPair)
						if cell.Latest != nil {
							tile.Source = gallerySourceOf(build, cell.Latest.FinishedAt)
						}
						row.Tiles = append(row.Tiles, tile)
					}
					g.Rows = append(g.Rows, row)
				}
			}
			return g, nil
		}
	}
	return Gallery{}, fmt.Errorf("%w: %s/%s", errNoBuild, suiteID, buildID)
}

func buildTile(cell suites.FlowCell, readPair func(app, flow, run, pair string) (diff.Summary, error)) GalleryTile {
	t := GalleryTile{Platform: cell.Platform, Status: cell.Status, Screens: []GalleryScreen{}, Wire: GalleryWire{Plane: "not-run", Note: noResultNote}}
	if cell.Latest == nil {
		return t
	}
	l := cell.Latest
	t.Planes, t.Reason, t.AttemptID = l.Planes, l.Reason, l.AttemptID
	if l.Evidence != nil {
		e := *l.Evidence
		t.Evidence = &e
	}
	for _, sc := range l.Screens {
		t.Screens = append(t.Screens, GalleryScreen{Label: sc.Label, SHA256: sc.SHA256, Media: sc.Media})
	}
	t.Wire = GalleryWire{Plane: l.Planes.Wire, Note: noPairNote}
	if l.WireNote != "" {
		t.Wire.Note = l.WireNote
	}
	if l.Evidence == nil || l.Evidence.PairID == "" {
		return t
	}
	e := l.Evidence
	p := &GalleryPair{App: e.App, Flow: e.Flow, RunID: e.RunID, PairID: e.PairID}
	t.Pair = p
	sum, err := readPair(e.App, e.Flow, e.RunID, e.PairID)
	if err != nil {
		p.Error = "linked comparison is not readable"
		if !errors.Is(err, os.ErrNotExist) {
			p.Error = err.Error()
		}
		t.Wire.Note = badPairNote
		return t
	}
	p.Available = true
	for _, cp := range sum.Checkpoints {
		p.Checkpoints = append(p.Checkpoints, GalleryCheckpoint{Name: cp.Name, Verdict: cp.Verdict, DiffPct: cp.DiffPct})
	}
	if n := len(p.Checkpoints); n > 0 {
		p.Checkpoint, p.Verdict = p.Checkpoints[n-1].Name, p.Checkpoints[n-1].Verdict
	}
	c := sum.Counts
	t.Wire = GalleryWire{Represented: true, Plane: l.Planes.Wire, Counts: &WireCounts{Paired: c.WirePaired, Changed: c.WireChanged,
		Moved: c.WireMoved, Missing: c.WireMissing, Extra: c.WireExtra, Violations: c.Violations}}
	return t
}

// WriteSuiteScreen serves one stored image for an immutable attempt. Same-origin
// SVG/HTML can run script, so only the raster types import accepts are served,
// with nosniff and a private, immutable cache header (the URL is content-addressed).
func WriteSuiteScreen(w http.ResponseWriter, cwd, suiteID, attemptID, sha string) {
	data, media, err := suites.OpenScreen(cwd, suiteID, attemptID, sha)
	if err != nil {
		code := http.StatusNotFound
		if errors.Is(err, os.ErrNotExist) {
			code = http.StatusNotFound
		}
		writeErr(w, code, "screen not available")
		return
	}
	w.Header().Set("Content-Type", media)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (s *server) handleSuiteScreen(w http.ResponseWriter, r *http.Request) {
	WriteSuiteScreen(w, s.deps().Cwd, r.PathValue("suite"), r.PathValue("attempt"), r.PathValue("sha"))
}
