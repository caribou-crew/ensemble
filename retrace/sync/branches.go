package sync

import (
	"errors"
	"os/exec"
	"sort"
	"time"
)

// DefaultBranchSince bounds how far back ListBranches looks for branches
// that have triggered a matching workflow — wider than DefaultSince (the
// pull path's 7-day window) because the branch this exists to surface (a
// rarely-dispatched workflow_dispatch, an ad hoc e2e/* push) may not have
// run again since.
const DefaultBranchSince = 30 * 24 * time.Hour

// BranchCandidate is one branch ListBranches found: its name and its most
// recent qualifying run's timestamp and triggering event. Unlike
// Candidate, this carries no per-run detail (no actor, no artifact count)
// — a picker needs to know a branch exists and roughly when it last ran,
// not the state of any one run on it, so ListBranches costs exactly one
// `gh run list` call per repo, no per-run fan-out.
type BranchCandidate struct {
	Name      string    `json:"name"`
	LastRunAt time.Time `json:"lastRunAt"`
	LastEvent string    `json:"lastEvent"`
}

// ListBranches discovers every branch that has triggered a workflow
// matching o.effectiveWorkflows() within o.Since (defaulting to
// DefaultBranchSince, not DefaultSince), one entry per branch naming its
// most recent qualifying run. o.Branches, when non-empty, is a glob
// allowlist applied to branch NAMES after discovery — see its own doc
// comment on Options for why this is distinct from o.Branch. A
// ListBranches call always clears Branch before calling listGitHubRuns,
// regardless of what the caller set, because it must see every branch.
func ListBranches(o Options) ([]BranchCandidate, error) {
	repos := o.effectiveRepos()
	if len(repos) == 0 {
		return nil, errors.New("sync: --repo is required for --from github")
	}
	if _, err := exec.LookPath("gh"); err != nil {
		return nil, errors.New("sync: the \"gh\" CLI is not on PATH — install it (https://cli.github.com) and run `gh auth login`, or set GH_TOKEN/GITHUB_TOKEN, before retrying")
	}
	workflows := o.effectiveWorkflows()
	if err := validateGlobPatterns(workflows); err != nil {
		return nil, err
	}
	if err := validateGlobPatterns(o.Branches); err != nil {
		return nil, err
	}

	since := o.Since
	if since <= 0 {
		since = DefaultBranchSince
	}
	cutoff := o.now().Add(-since)

	byBranch := make(map[string]BranchCandidate)
	for _, repo := range repos {
		o2 := o
		o2.Branch = "" // must see every branch, never narrowed to one
		list, err := listGitHubRuns(o2, repo)
		if err != nil {
			return nil, err
		}
		for _, r := range list {
			if !matchesGlob(r.WorkflowName, workflows) {
				continue
			}
			if r.CreatedAt.Before(cutoff) {
				continue
			}
			if !matchesGlob(r.HeadBranch, o.Branches) {
				continue
			}
			cur, ok := byBranch[r.HeadBranch]
			if !ok || r.CreatedAt.After(cur.LastRunAt) {
				byBranch[r.HeadBranch] = BranchCandidate{Name: r.HeadBranch, LastRunAt: r.CreatedAt, LastEvent: r.Event}
			}
		}
	}

	out := make([]BranchCandidate, 0, len(byBranch))
	for _, b := range byBranch {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastRunAt.After(out[j].LastRunAt) })
	return out, nil
}
