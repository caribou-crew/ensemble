package serve

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/caribou-crew/ensemble/retrace/runs"
)

// evidenceVideoExts is the allow-listed set of video file extensions this
// package will list and serve.
var evidenceVideoExts = []string{".webm", ".mp4"}

// evidenceRunDir resolves the CANDIDATE ("b") run's directory for app/flow
// — the run currently under review, and the only one evidence is ever
// attached to (design doc D4: a reference run's old video/report adds
// nothing a pixel/wire diff doesn't already show).
//
// This deliberately does NOT go through SummaryFor/diff.Build: a flow with
// a recorded run but no accepted reference yet still has a candidate run
// whose evidence is worth viewing, and summaryFor refuses before evidence
// would ever be reached (see queue.go's "no reference" error). Evidence is
// resolved the same way summaryFor resolves its own B side — FindRun
// "latest" + PathsFor — just without requiring an A side to exist.
func evidenceRunDir(d Deps, app, flow string) (string, error) {
	if err := runs.ValidateComponents(app, flow); err != nil {
		return "", err
	}
	root := runs.RunsRoot(d.Cwd)
	id := runs.FindRun(root, app, flow, "latest")
	if id == "" {
		return "", fmt.Errorf("no run recorded for %s/%s", app, flow)
	}
	p, err := runs.PathsFor(root, app, flow, id)
	if err != nil {
		return "", err
	}
	return p.RunDir, nil
}

// Evidence is what GET .../evidence/{app}/{flow} reports: what's available
// to view, not the files themselves.
type Evidence struct {
	// Videos is never nil — always [] when none exist, so a client can
	// render "no video" without special-casing null.
	Videos    []string `json:"videos"`
	HasReport bool     `json:"hasReport"`
}

// WriteEvidence lists the candidate run's videos/ directory and reports
// whether report/index.html exists. A directory listing, not a manifest
// field — see the design doc's D1: evidence is attached after `retrace
// run` exits, so it cannot live in the one-writer manifest.json.
func WriteEvidence(w http.ResponseWriter, d Deps, app, flow string) {
	dir, err := evidenceRunDir(d, app, flow)
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	videos, err := listVideos(filepath.Join(dir, "videos"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_, statErr := os.Stat(filepath.Join(dir, "report", "index.html"))
	writeJSON(w, http.StatusOK, Evidence{Videos: videos, HasReport: statErr == nil})
}

func listVideos(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		for _, want := range evidenceVideoExts {
			if ext == want {
				out = append(out, e.Name())
				break
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

// WriteVideo serves one file from the candidate run's videos/ directory.
// http.ServeContent, not os.ReadFile (unlike WriteShot): a <video> element
// seeks by issuing Range requests, and ServeContent is what answers them
// with 206 Partial Content instead of re-sending the whole file every seek.
func WriteVideo(w http.ResponseWriter, r *http.Request, d Deps, app, flow, name string) {
	base, err := safeBase(name)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	dir, err := evidenceRunDir(d, app, flow)
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	f, err := os.Open(filepath.Join(dir, "videos", base))
	if errors.Is(err, os.ErrNotExist) {
		writeErr(w, http.StatusNotFound, fmt.Sprintf("no video %q for %s/%s", name, app, flow))
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	http.ServeContent(w, r, base, info.ModTime(), f)
}

// safeSubpath is safeBase's multi-segment sibling: it permits internal "/"
// (a report is a directory of assets, e.g. "assets/app.js"), but the same
// "root at / then Clean" technique still makes an escape attempt collapse
// to something that starts with "..", which is rejected.
func safeSubpath(rel string) (string, error) {
	clean := path.Clean("/" + rel)
	sub := strings.TrimPrefix(clean, "/")
	if strings.HasPrefix(sub, "..") {
		return "", fmt.Errorf("invalid report path %q", rel)
	}
	return sub, nil
}

// WriteReport serves the candidate run's report/ directory as a static
// file tree — Playwright's HTML report is a directory of assets
// (index.html plus its own JS/CSS/data files), not one file, unlike
// WriteShot's single ReadFile.
func WriteReport(w http.ResponseWriter, r *http.Request, d Deps, app, flow string) {
	dir, err := evidenceRunDir(d, app, flow)
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	reportDir := filepath.Join(dir, "report")
	if _, err := os.Stat(filepath.Join(reportDir, "index.html")); errors.Is(err, os.ErrNotExist) {
		writeErr(w, http.StatusNotFound, fmt.Sprintf("no report for %s/%s", app, flow))
		return
	}

	rel := r.PathValue("path")
	if rel == "" {
		rel = "index.html"
	}
	sub, err := safeSubpath(rel)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	http.ServeFile(w, r, filepath.Join(reportDir, sub))
}
