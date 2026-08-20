// Package runs owns the on-disk shape of a retrace recording: where a run
// directory lives, what its manifest says, and how flow-part markers are
// written and folded into intervals. It knows nothing about proxies or
// diffing, so capture, diff, refs and serve can all depend on it.
package runs

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Schema versions the manifest, independently of core/trace's hop schema:
// a manifest gains fields far more often than a hop record does.
const Schema = "retrace/1"

// RefRunID is the fixed run-id level inside a reference bundle. Literal,
// never the source runId: a churning directory name makes git show each
// promotion as a delete + add instead of a screenshot modification.
const RefRunID = "reference"

func RunsRoot(cwd string) string { return filepath.Join(cwd, ".retrace", "runs") }
func RefsRoot(cwd string) string { return filepath.Join(cwd, ".retrace-ref") }

// NewRunID is timestamp-first so lexical directory order is chronological
// order — every listing in this package relies on that and never stats.
func NewRunID(now time.Time, sha string) string {
	stamp := now.UTC().Format("20060102T150405Z")
	short := "nogit"
	if len(sha) >= 7 {
		short = sha[:7]
	} else if sha != "" {
		short = sha
	}
	return stamp + "-" + short
}

// Paths is every file a run directory can hold. Members are absolute once
// PathsFor is given an absolute root; existence is never implied.
type Paths struct {
	RunDir       string
	ManifestPath string
	ShotsDir     string
	WirePath     string // client-edge hops, NDJSON of trace.Hop
	HopsPath     string // full provider chain, NDJSON of trace.Hop
	GroupsPath   string
	MissesPath   string // replay only
}

func PathsFor(root, app, flow, runID string) Paths {
	dir := filepath.Join(root, app, flow, runID)
	return Paths{
		RunDir:       dir,
		ManifestPath: filepath.Join(dir, "manifest.json"),
		ShotsDir:     filepath.Join(dir, "shots"),
		WirePath:     filepath.Join(dir, "wire.jsonl"),
		HopsPath:     filepath.Join(dir, "hops.jsonl"),
		GroupsPath:   filepath.Join(dir, "groups.jsonl"),
		MissesPath:   filepath.Join(dir, "misses.jsonl"),
	}
}

func Create(root, app, flow, runID string) (Paths, error) {
	p := PathsFor(root, app, flow, runID)
	if err := os.MkdirAll(p.ShotsDir, 0o755); err != nil {
		return Paths{}, err
	}
	return p, nil
}

func dirNames(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil // a root that was never written is empty, not an error
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

func ListApps(root string) []string            { return dirNames(root) }
func ListFlows(root, app string) []string      { return dirNames(filepath.Join(root, app)) }
func ListRuns(root, app, flow string) []string { return dirNames(filepath.Join(root, app, flow)) }

// FindRun resolves a user-facing selector: "latest", an exact run id, or a
// git sha (full or short) whose run id ends in its 7-char prefix. Returns
// "" when nothing matches — callers report that, never guess.
func FindRun(root, app, flow, selector string) string {
	ids := ListRuns(root, app, flow)
	if len(ids) == 0 {
		return ""
	}
	if selector == "" || selector == "latest" {
		return ids[len(ids)-1]
	}
	for _, id := range ids {
		if id == selector {
			return id
		}
	}
	short := selector
	if len(short) > 7 {
		short = short[:7]
	}
	for i := len(ids) - 1; i >= 0; i-- {
		if strings.HasSuffix(ids[i], "-"+short) {
			return ids[i]
		}
	}
	return ""
}
