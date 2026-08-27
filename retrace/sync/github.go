package sync

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/caribou-crew/ensemble/retrace/runs"
)

// ghRun is one row of `gh run list --json databaseId,workflowName,headSha,
// url,createdAt`. Field names are exactly gh's own JSON keys, so this
// struct decodes gh's output with no field remapping to get wrong.
type ghRun struct {
	DatabaseID   int64     `json:"databaseId"`
	WorkflowName string    `json:"workflowName"`
	HeadSHA      string    `json:"headSha"`
	URL          string    `json:"url"`
	CreatedAt    time.Time `json:"createdAt"`
}

// runGitHub is the "github" backend Run dispatches to.
//
// Since filtering happens HERE, in Go, against the CreatedAt gh already
// reports — not via a `gh run list` date flag — so the same window logic
// is exercised whether the fixture behind `gh` is real or a test double,
// and a test can inject Now without gh's own clock getting a vote.
func runGitHub(o Options) (Result, error) {
	if o.Repo == "" {
		return Result{}, errors.New("sync: --repo is required for --from github")
	}
	if _, err := exec.LookPath("gh"); err != nil {
		return Result{}, errors.New("sync: the \"gh\" CLI is not on PATH — install it (https://cli.github.com) and run `gh auth login`, or set GH_TOKEN/GITHUB_TOKEN, before retrying")
	}

	list, err := listGitHubRuns(o)
	if err != nil {
		return Result{}, err
	}

	result := Result{Synced: []string{}, Skipped: []SkipReason{}}
	cutoff := o.now().Add(-o.since())
	for _, r := range list {
		if r.CreatedAt.Before(cutoff) {
			continue
		}
		synced, skipped, err := syncOneRun(o, r)
		if err != nil {
			return Result{}, err
		}
		result.Synced = append(result.Synced, synced...)
		result.Skipped = append(result.Skipped, skipped...)
	}
	return result, nil
}

func listGitHubRuns(o Options) ([]ghRun, error) {
	args := []string{"run", "list", "--repo", o.Repo, "--json", "databaseId,workflowName,headSha,url,createdAt", "--limit", "100"}
	if o.Workflow != "" {
		args = append(args, "--workflow", o.Workflow)
	}
	out, err := exec.Command("gh", args...).Output()
	if err != nil {
		return nil, fmt.Errorf("sync: gh run list: %w%s", err, stderrOf(err))
	}
	var list []ghRun
	if err := json.Unmarshal(out, &list); err != nil {
		return nil, fmt.Errorf("sync: parsing gh run list output: %w", err)
	}
	return list, nil
}

// stderrOf formats an *exec.ExitError's captured stderr, when there is
// one. exec.Cmd.Output populates ExitError.Stderr automatically when
// Cmd.Stderr was left nil, which is the case here — this is the only
// place that reads it.
func stderrOf(err error) string {
	var ee *exec.ExitError
	if errors.As(err, &ee) && len(ee.Stderr) > 0 {
		return ": " + string(ee.Stderr)
	}
	return ""
}

// syncOneRun downloads one workflow run's artifacts into a scratch
// directory, then merges every run bundle it finds. A run whose artifacts
// contain no manifest.json at all becomes a SkipReason, never an error
// that aborts the rest of the sync (mirrors retrace/serve's own rule that
// one broken flow must not take a whole queue down).
func syncOneRun(o Options, r ghRun) (synced []string, skipped []SkipReason, err error) {
	tmp, err := os.MkdirTemp("", "retrace-sync-*")
	if err != nil {
		return nil, nil, fmt.Errorf("sync: creating a scratch directory: %w", err)
	}
	defer os.RemoveAll(tmp)

	dlArgs := []string{"run", "download", strconv.FormatInt(r.DatabaseID, 10), "--repo", o.Repo, "--dir", tmp}
	if out, err := exec.Command("gh", dlArgs...).CombinedOutput(); err != nil {
		return nil, nil, fmt.Errorf("sync: gh run download %d: %w: %s", r.DatabaseID, err, string(out))
	}

	manifests, err := findManifests(tmp)
	if err != nil {
		return nil, nil, err
	}
	label := fmt.Sprintf("run %d (%s)", r.DatabaseID, r.WorkflowName)
	if len(manifests) == 0 {
		return nil, []SkipReason{{
			Artifact: label,
			Reason:   "no <app>/<flow>/<run-id>/manifest.json found anywhere in the downloaded artifact(s)",
		}}, nil
	}

	for _, m := range manifests {
		runDir := filepath.Dir(m)
		runID := filepath.Base(runDir)
		flowDir := filepath.Dir(runDir)
		flow := filepath.Base(flowDir)
		app := filepath.Base(filepath.Dir(flowDir))
		if app == "." || app == string(filepath.Separator) || flow == "." {
			skipped = append(skipped, SkipReason{
				Artifact: label,
				Reason:   fmt.Sprintf("manifest.json at an unexpected depth (need <app>/<flow>/<run-id>/manifest.json): %s", m),
			})
			continue
		}

		dest := filepath.Join(runs.RunsRoot(o.Cwd), app, flow, runID)
		if _, statErr := os.Stat(dest); statErr == nil {
			continue // already synced — directory presence is the whole idempotency check, see design D6
		}

		if err := copyTree(runDir, dest); err != nil {
			return synced, skipped, fmt.Errorf("sync: copying %s/%s/%s: %w", app, flow, runID, err)
		}
		if err := runs.WriteSource(runs.Paths{RunDir: dest}, runs.Source{
			Kind: runs.SourceKindCI, Workflow: r.WorkflowName, RunURL: r.URL, SHA: r.HeadSHA, SyncedAt: o.now(),
		}); err != nil {
			return synced, skipped, fmt.Errorf("sync: writing source.json for %s/%s/%s: %w", app, flow, runID, err)
		}
		synced = append(synced, filepath.Join(app, flow, runID))
	}
	return synced, skipped, nil
}

// findManifests returns every manifest.json under root, sorted so two
// runs of this function over the same tree agree on order — a directory
// walk's OS-reported order is not itself a contract worth relying on.
func findManifests(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && d.Name() == "manifest.json" {
			out = append(out, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("sync: walking downloaded artifact: %w", err)
	}
	sort.Strings(out)
	return out, nil
}

// copyTree copies src's tree into dest. The caller (syncOneRun) has
// already confirmed dest does not exist — this never overwrites.
func copyTree(src, dest string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dest, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(path, target)
	})
}

func copyFile(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := out.ReadFrom(in); err != nil {
		return err
	}
	return out.Close()
}
