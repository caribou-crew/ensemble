// Package sync pulls CI-recorded retrace runs onto local disk, into the
// same `.retrace/runs/<app>/<flow>/<run-id>/` tree `retrace run` writes to
// — so the existing review queue (retrace/serve) and diff engine
// (retrace/diff) handle a synced run identically to a local one, with no
// second implementation of verdict logic anywhere in this package.
//
// See openspec/changes/retrace-ci-sync/design.md for why this shells out
// to the `gh` CLI rather than calling the GitHub API directly, and why
// provenance is a sidecar file (retrace/runs.Source) rather than a
// Manifest field.
package sync

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// DefaultSince is how far back `retrace sync` looks for qualifying
// workflow runs when Options.Since is zero.
const DefaultSince = 7 * 24 * time.Hour

// Options configures one sync call. It is the input both the CLI's
// `retrace sync` command and ensemble's `POST /api/retrace/sync` route
// build and pass to Run — there is exactly one place that decides what a
// sync does with them.
type Options struct {
	// Cwd is the project root — the same directory `retrace run` and
	// `retrace serve` resolve `.retrace/runs/` against.
	Cwd string
	// From selects the backend. Only "github" is implemented; any other
	// value is refused before any network call.
	From string
	// Repo is "org/repo" — required for the github backend.
	Repo string
	// Workflow optionally narrows sync to one GitHub Actions workflow
	// name. Empty means every workflow in Repo.
	Workflow string
	// Repos, when non-empty, syncs every listed "org/repo" in turn — the
	// plural form of Repo. Repo remains the single-value alias; use
	// effectiveRepos to read either uniformly.
	Repos []string
	// Workflows, when non-empty, narrows sync to any GitHub Actions
	// workflow name matching one of these entries — each may be an exact
	// name or a path.Match glob (e.g. "Retrace *"). Workflow remains the
	// single-value alias; use effectiveWorkflows to read either
	// uniformly.
	Workflows []string
	// Branch narrows sync to runs off this branch (gh run list --branch).
	Branch string
	// Branches narrows ListBranches's output to branch names matching one
	// of these glob patterns (path.Match, same mechanism as Workflows).
	// Distinct from Branch: Branch is a single exact-match filter Run/List
	// pass straight to `gh run list --branch`, narrowing what is fetched in
	// the first place; Branches is applied in Go, after fetching, against
	// every branch ListBranches discovers. Empty means no filter — every
	// branch found is returned. Unused by Run and List.
	Branches []string
	// Actor narrows sync to runs triggered by this GitHub user (gh run
	// list --user).
	Actor string
	// Event narrows sync to runs triggered by this event, e.g. "push" or
	// "schedule" (gh run list --event).
	Event string
	// Status narrows sync by run status or conclusion, e.g. "completed"
	// or "failure" (gh run list --status — gh itself accepts either kind
	// of value through this one flag).
	Status string
	// Since bounds how far back to look for qualifying workflow runs.
	// Zero means DefaultSince.
	Since time.Duration
	// Now is the reference clock Since is measured against, and the clock
	// stamped into each synced run's source.json. Nil means time.Now —
	// injectable so a test's window doesn't race the wall clock.
	Now func() time.Time
	// Selections, when non-empty, restricts Run to exactly these
	// (repo, databaseId) pairs — see List and Selection's own doc
	// comments. Added by Task 5; declared here so Task 5 is a pure
	// addition, not a struct-shape change other tasks must chase.
	Selections []Selection
	// Apps, when non-empty, restricts the merge step to run directories
	// for these app keys: a downloaded artifact's <app>/<flow>/<run-id>/
	// directory is merged only when <app> is in this list, and reported
	// in Result.Skipped (with a reason distinct from a malformed
	// artifact) otherwise. Empty — every caller before this field
	// existed — means no filter: every run directory an artifact
	// contains is merged, unchanged.
	//
	// This exists for `retrace serve --watch` (retrace/repoconfig): a
	// repo whose apps are recorded across more than one root must sync
	// each root separately, and without this an artifact containing
	// several apps' run directories would get ALL of them merged into
	// every root that happens to sync it — see
	// openspec/changes/retrace-repo-config/design.md decision D4.
	Apps []string
}

// appAllowed reports whether app may be merged: true when Apps is empty
// (no filter — the default for every caller that doesn't set it), or when
// app is one of the names Apps lists.
func (o Options) appAllowed(app string) bool {
	if len(o.Apps) == 0 {
		return true
	}
	return slices.Contains(o.Apps, app)
}

func (o Options) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

func (o Options) since() time.Duration {
	if o.Since > 0 {
		return o.Since
	}
	return DefaultSince
}

// effectiveRepos returns Repos, or a one-element slice of Repo when Repos
// is empty, or nil when neither is set.
func (o Options) effectiveRepos() []string {
	if len(o.Repos) > 0 {
		return o.Repos
	}
	if o.Repo != "" {
		return []string{o.Repo}
	}
	return nil
}

// effectiveWorkflows returns Workflows, or a one-element slice of
// Workflow when Workflows is empty, or nil (meaning "no workflow filter")
// when neither is set.
func (o Options) effectiveWorkflows() []string {
	if len(o.Workflows) > 0 {
		return o.Workflows
	}
	if o.Workflow != "" {
		return []string{o.Workflow}
	}
	return nil
}

func (o Options) hasSelections() bool { return len(o.Selections) > 0 }

func (o Options) isSelected(repo string, databaseID int64) bool {
	for _, s := range o.Selections {
		if s.Repo == repo && s.DatabaseID == databaseID {
			return true
		}
	}
	return false
}

// Selection names one candidate run a caller already picked from a prior
// List call — (Repo, DatabaseID) rather than a bare ID, because GitHub
// Actions run IDs are unique platform-wide but Run still needs to know
// which repo to call `gh run download --repo` against without
// re-deriving it from a window search.
type Selection struct {
	Repo       string `json:"repo"`
	DatabaseID int64  `json:"databaseId"`
}

// SkipReason names one artifact sync declined to merge, and why. Reported
// rather than silently dropped, for the same reason retrace/serve reports
// a broken run as a quarantined item instead of omitting the row: an
// artifact silently missing from a sync report is indistinguishable from
// one that was never uploaded at all.
type SkipReason struct {
	Artifact string `json:"artifact"`
	Reason   string `json:"reason"`
}

// Result is what `retrace sync --json` prints, and what `POST
// /api/retrace/sync` returns as its body.
type Result struct {
	// Synced is every run ("<app>/<flow>/<run-id>") newly merged onto
	// local disk by this call. Never nil: an omitted array and an empty
	// one read identically to a human, but `results == null` and
	// `results == []` are NOT the same fact to a client that checks
	// `.length`, and this is exactly the zero-value trap
	// retrace/serve.Item.Gates's own doc names.
	Synced []string `json:"synced"`
	// Skipped names every artifact that could not be merged, and why.
	// Never nil, for the same reason Synced isn't.
	Skipped []SkipReason `json:"skipped"`
}

// ParseSince parses a `--since` value — "7d", "24h", "30m", "45s" — into a
// time.Duration. It exists because "d" (days) is not a unit
// time.ParseDuration accepts, and --since 7d is the form both the CLI's
// usage text and the proposal that motivated this package use throughout.
func ParseSince(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if days, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("sync: invalid --since %q: want a positive integer before \"d\"", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("sync: invalid --since %q: %w", s, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("sync: --since must be positive, got %q", s)
	}
	return d, nil
}

// Run performs one sync. It is the ONE implementation the CLI command and
// the ensemble REST route both call — see the package doc comment.
func Run(o Options) (Result, error) {
	if strings.TrimSpace(o.Cwd) == "" {
		return Result{}, errors.New("sync: Cwd is empty — a sync with no project root would resolve .retrace/runs against the process working directory")
	}
	switch o.From {
	case "github":
		return runGitHub(o)
	case "s3":
		return Result{}, errors.New("sync: --from s3 is not supported yet — see openspec/changes/retrace-ci-sync/proposal.md's deferred S3 backend")
	default:
		return Result{}, fmt.Errorf("sync: unknown --from %q (only \"github\" is supported)", o.From)
	}
}
