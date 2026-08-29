// Package sync's candidates.go is the "discover" half of discover →
// filter → select → pull: List answers "what's out there" without ever
// calling `gh run download`, so a human (or the dashboard) can choose
// before anything is pulled — see Run for the actual pull.
package sync

import (
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Candidate is one workflow run List surfaces for browsing. It carries
// enough to judge a run without downloading anything: HasArtifacts and
// Actor both cost one extra `gh api` call each (gh run list's own JSON
// output has neither — verified against gh 2.87.3's --json field list),
// so List is only as fast as its filters are narrow.
type Candidate struct {
	Repo         string    `json:"repo"`
	DatabaseID   int64     `json:"databaseId"`
	WorkflowName string    `json:"workflowName"`
	HeadBranch   string    `json:"headBranch"`
	Actor        string    `json:"actor"`
	Event        string    `json:"event"`
	Status       string    `json:"status"`
	Conclusion   string    `json:"conclusion"`
	CreatedAt    time.Time `json:"createdAt"`
	URL          string    `json:"url"`
	HasArtifacts bool      `json:"hasArtifacts"`
}

// List discovers candidate workflow runs across o's configured repo(s).
// It applies every filter Run itself would (since, branch, actor, event,
// status via gh's own flags; workflow name/glob in Go) but never calls
// `gh run download` — unlike Run, a non-completed or artifact-less run is
// reported here, not skipped, because seeing exactly that is the point.
// Candidates are sorted newest first.
func List(o Options) ([]Candidate, error) {
	if strings.TrimSpace(o.Cwd) == "" {
		return nil, errors.New("sync: Cwd is empty — a sync with no project root would resolve .retrace/runs against the process working directory")
	}
	if o.From != "" && o.From != "github" {
		return nil, fmt.Errorf("sync: unknown --from %q (only \"github\" is supported)", o.From)
	}
	repos := o.effectiveRepos()
	if len(repos) == 0 {
		return nil, errors.New("sync: --repo is required for --from github")
	}
	if _, err := exec.LookPath("gh"); err != nil {
		return nil, errors.New("sync: the \"gh\" CLI is not on PATH — install it (https://cli.github.com) and run `gh auth login`, or set GH_TOKEN/GITHUB_TOKEN, before retrying")
	}
	workflows := o.effectiveWorkflows()
	if err := validateWorkflowPatterns(workflows); err != nil {
		return nil, err
	}

	cutoff := o.now().Add(-o.since())
	candidates := []Candidate{}
	for _, repo := range repos {
		list, err := listGitHubRuns(o, repo)
		if err != nil {
			return nil, err
		}
		for _, r := range list {
			if !matchesWorkflow(r.WorkflowName, workflows) {
				continue
			}
			if r.CreatedAt.Before(cutoff) {
				continue
			}
			actor, err := fetchActor(repo, r.DatabaseID)
			if err != nil {
				return nil, err
			}
			has, err := runHasArtifacts(repo, r.DatabaseID)
			if err != nil {
				return nil, err
			}
			candidates = append(candidates, Candidate{
				Repo: repo, DatabaseID: r.DatabaseID, WorkflowName: r.WorkflowName,
				HeadBranch: r.HeadBranch, Actor: actor, Event: r.Event,
				Status: r.Status, Conclusion: r.Conclusion, CreatedAt: r.CreatedAt,
				URL: r.URL, HasArtifacts: has,
			})
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].CreatedAt.After(candidates[j].CreatedAt) })
	return candidates, nil
}

// runHasArtifacts reports whether GitHub has any artifact recorded
// against run databaseID in repo — checked via the artifacts endpoint's
// total_count, not by downloading, so List can say "0 artifacts" without
// pulling anything.
func runHasArtifacts(repo string, databaseID int64) (bool, error) {
	out, err := exec.Command("gh", "api",
		fmt.Sprintf("repos/%s/actions/runs/%d/artifacts", repo, databaseID),
		"--jq", ".total_count").Output()
	if err != nil {
		return false, fmt.Errorf("sync: gh api (artifacts for run %d): %w%s", databaseID, err, stderrOf(err))
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return false, fmt.Errorf("sync: parsing artifact count for run %d: %w", databaseID, err)
	}
	return n > 0, nil
}
