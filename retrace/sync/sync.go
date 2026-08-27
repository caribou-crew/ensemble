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
	// Since bounds how far back to look for qualifying workflow runs.
	// Zero means DefaultSince.
	Since time.Duration
	// Now is the reference clock Since is measured against, and the clock
	// stamped into each synced run's source.json. Nil means time.Now —
	// injectable so a test's window doesn't race the wall clock.
	Now func() time.Time
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
