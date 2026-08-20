// Package runs owns the on-disk shape of a retrace recording: where a run
// directory lives, what its manifest says, and how flow-part markers are
// written and folded into intervals. It knows nothing about proxies or
// diffing, so capture, diff, refs and serve can all depend on it.
package runs

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
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
//
// Paths is deliberately not a wire type — it holds absolute host
// filesystem paths (e.g. "/Users/steven/.../shots/cart.png"). A REST
// handler that wants to expose a run's files builds run-dir-relative
// strings itself; it must never marshal a Paths directly.
type Paths struct {
	RunDir       string
	ManifestPath string
	ShotsDir     string
	WirePath     string // client-edge hops, NDJSON of trace.Hop
	HopsPath     string // full provider chain, NDJSON of trace.Hop
	GroupsPath   string
	MissesPath   string // replay only
}

// validComponent matches one path-safe app/flow/runID component: no
// separators, no traversal, nothing a filesystem or URL router would treat
// specially.
var validComponent = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// validateComponent rejects any app/flow/runID value that could escape the
// runs/refs root once joined into a path: empty, a leading dot (which
// catches "." and ".." too), an embedded separator, or any rune outside
// [A-Za-z0-9._-]. All three values can originate from an HTTP request
// (Task 13's review server routes /api/runs/{app}/{flow}/{run}/... straight
// into PathsFor), and net/http.ServeMux cleans the path AFTER routing on
// the still-escaped string — see global-constraints.md. Validating once,
// here, is what keeps three later tasks from each writing their own copy.
func validateComponent(name string) error {
	if name == "" || strings.HasPrefix(name, ".") || strings.ContainsAny(name, `/\`) || !validComponent.MatchString(name) {
		return fmt.Errorf("runs: invalid path component %q", name)
	}
	return nil
}

// PathsFor computes the paths a run directory would have, without
// touching disk. It validates app/flow/runID (see validateComponent) so
// every caller — Create, and every later task that resolves an existing
// run from a selector — gets the same traversal guard from one place.
func PathsFor(root, app, flow, runID string) (Paths, error) {
	for _, c := range [...]string{app, flow, runID} {
		if err := validateComponent(c); err != nil {
			return Paths{}, err
		}
	}
	dir := filepath.Join(root, app, flow, runID)
	return Paths{
		RunDir:       dir,
		ManifestPath: filepath.Join(dir, "manifest.json"),
		ShotsDir:     filepath.Join(dir, "shots"),
		WirePath:     filepath.Join(dir, "wire.jsonl"),
		HopsPath:     filepath.Join(dir, "hops.jsonl"),
		GroupsPath:   filepath.Join(dir, "groups.jsonl"),
		MissesPath:   filepath.Join(dir, "misses.jsonl"),
	}, nil
}

// Create makes a fresh run directory and its shots subdirectory. It fails
// if the run directory already exists — two runs must never silently share
// one directory. A caller that gets a "file exists" error back must pick a
// new run id or treat it as a genuine conflict, never retry into the same
// files (a CI matrix running the same flow twice inside one second is
// exactly the case NewRunID's 1s resolution can produce).
func Create(root, app, flow, runID string) (Paths, error) {
	p, err := PathsFor(root, app, flow, runID)
	if err != nil {
		return Paths{}, err
	}
	if err := os.MkdirAll(filepath.Dir(p.RunDir), 0o755); err != nil {
		return Paths{}, err
	}
	if err := os.Mkdir(p.RunDir, 0o755); err != nil {
		return Paths{}, err
	}
	if err := os.Mkdir(p.ShotsDir, 0o755); err != nil {
		return Paths{}, err
	}
	return p, nil
}

// dirNames lists subdirectories of dir. A root that was never written is
// legitimately empty; every other failure (wrong permissions, a broken
// mount, dir being a regular file) is also reported as empty here, because
// ListApps/ListFlows/ListRuns' locked []string signature has no room for
// an error. A command entrypoint that must tell "no runs recorded" apart
// from "can't read the runs root" should call the *Err sibling below
// instead of trusting an empty list.
func dirNames(dir string) []string {
	out, err := dirNamesErr(dir)
	if err != nil {
		return nil
	}
	return out
}

func dirNamesErr(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil // a root that was never written is empty, not an error
	}
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

func ListApps(root string) []string            { return dirNames(root) }
func ListFlows(root, app string) []string      { return dirNames(filepath.Join(root, app)) }
func ListRuns(root, app, flow string) []string { return dirNames(filepath.Join(root, app, flow)) }

// ListAppsErr, ListFlowsErr and ListRunsErr surface a real I/O error
// (permission denied, a broken mount, root being a regular file) instead of
// silently returning empty — the difference between "no runs recorded" and
// "can't read the runs root", which must not look alike to a CI gate.
// Command entrypoints should call these; ListApps/ListFlows/ListRuns stay
// for callers that only ever want "what's here right now".
func ListAppsErr(root string) ([]string, error) { return dirNamesErr(root) }
func ListFlowsErr(root, app string) ([]string, error) {
	return dirNamesErr(filepath.Join(root, app))
}
func ListRunsErr(root, app, flow string) ([]string, error) {
	return dirNamesErr(filepath.Join(root, app, flow))
}

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
