package sync

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/caribou-crew/ensemble/retrace/runs"
)

// ghRun is one row of `gh run list --json databaseId,workflowName,headSha,
// headBranch,event,status,conclusion,url,createdAt`. Field names are
// exactly gh's own JSON keys, so this struct decodes gh's output with no
// field remapping to get wrong.
type ghRun struct {
	DatabaseID   int64     `json:"databaseId"`
	WorkflowName string    `json:"workflowName"`
	HeadBranch   string    `json:"headBranch"`
	HeadSHA      string    `json:"headSha"`
	Event        string    `json:"event"`
	Status       string    `json:"status"`
	Conclusion   string    `json:"conclusion"`
	URL          string    `json:"url"`
	CreatedAt    time.Time `json:"createdAt"`
}

// runLabel names one run for a SkipReason — "run <id> (<workflow>)".
func runLabel(r ghRun) string {
	return fmt.Sprintf("run %d (%s)", r.DatabaseID, r.WorkflowName)
}

// runGitHub is the "github" backend Run dispatches to.
//
// Since filtering happens HERE, in Go, against the CreatedAt gh already
// reports — not via a `gh run list` date flag — so the same window logic
// is exercised whether the fixture behind `gh` is real or a test double,
// and a test can inject Now without gh's own clock getting a vote.
func runGitHub(o Options) (Result, error) {
	repos := o.effectiveRepos()
	if len(repos) == 0 {
		return Result{}, errors.New("sync: --repo is required for --from github")
	}
	if _, err := exec.LookPath("gh"); err != nil {
		return Result{}, errors.New("sync: the \"gh\" CLI is not on PATH — install it (https://cli.github.com) and run `gh auth login`, or set GH_TOKEN/GITHUB_TOKEN, before retrying")
	}
	workflows := o.effectiveWorkflows()
	if err := validateWorkflowPatterns(workflows); err != nil {
		return Result{}, err
	}

	result := Result{Synced: []string{}, Skipped: []SkipReason{}}
	cutoff := o.now().Add(-o.since())
	for _, repo := range repos {
		list, err := listGitHubRuns(o, repo)
		if err != nil {
			return Result{}, err
		}
		for _, r := range list {
			if !matchesWorkflow(r.WorkflowName, workflows) {
				continue
			}
			if o.hasSelections() {
				if !o.isSelected(repo, r.DatabaseID) {
					continue
				}
			} else if r.CreatedAt.Before(cutoff) {
				continue
			}
			if r.Status != "completed" {
				result.Skipped = append(result.Skipped, SkipReason{
					Artifact: runLabel(r),
					Reason:   fmt.Sprintf("run is %s, not completed — skipping rather than risking a doomed download", r.Status),
				})
				continue
			}
			synced, skipped, err := syncOneRun(o, repo, r)
			if err != nil {
				return Result{}, err
			}
			result.Synced = append(result.Synced, synced...)
			result.Skipped = append(result.Skipped, skipped...)
		}
	}
	return result, nil
}

func listGitHubRuns(o Options, repo string) ([]ghRun, error) {
	args := []string{"run", "list", "--repo", repo, "--json", "databaseId,workflowName,headSha,headBranch,event,status,conclusion,url,createdAt", "--limit", "100"}
	if o.Branch != "" {
		args = append(args, "--branch", o.Branch)
	}
	if o.Actor != "" {
		args = append(args, "--user", o.Actor)
	}
	if o.Event != "" {
		args = append(args, "--event", o.Event)
	}
	if o.Status != "" {
		args = append(args, "--status", o.Status)
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

// matchesWorkflow reports whether name satisfies patterns — every pattern
// is tried as a path.Match glob (so an exact name with no glob
// metacharacter just matches itself), and an empty patterns list means
// "no workflow filter", matching everything.
func matchesWorkflow(name string, patterns []string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, p := range patterns {
		if ok, _ := path.Match(p, name); ok {
			return true
		}
	}
	return false
}

// validateWorkflowPatterns rejects a malformed glob up front, so a
// typo'd pattern fails the sync loudly instead of silently matching
// nothing.
func validateWorkflowPatterns(patterns []string) error {
	for _, p := range patterns {
		if _, err := path.Match(p, ""); err != nil {
			return fmt.Errorf("sync: invalid workflow pattern %q: %w", p, err)
		}
	}
	return nil
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

// fetchActor returns the GitHub login of the user who triggered run
// databaseID in repo. gh run list's own --json output has no actor field
// (verified against gh 2.87.3's --json field list), so this costs one
// extra `gh api` call — used by List (browsing) and syncOneRun
// (provenance), never by the window-based pull's hot loop for a run it
// isn't actually about to merge.
func fetchActor(repo string, databaseID int64) (string, error) {
	out, err := exec.Command("gh", "api",
		fmt.Sprintf("repos/%s/actions/runs/%d", repo, databaseID),
		"--jq", ".actor.login").Output()
	if err != nil {
		return "", fmt.Errorf("sync: gh api (actor for run %d): %w%s", databaseID, err, stderrOf(err))
	}
	return strings.TrimSpace(string(out)), nil
}

// syncOneRun downloads one workflow run's artifacts into a scratch
// directory, then merges every run bundle it finds. A run whose artifacts
// contain no manifest.json at all becomes a SkipReason, never an error
// that aborts the rest of the sync (mirrors retrace/serve's own rule that
// one broken flow must not take a whole queue down).
func syncOneRun(o Options, repo string, r ghRun) (synced []string, skipped []SkipReason, err error) {
	tmp, err := os.MkdirTemp("", "retrace-sync-*")
	if err != nil {
		return nil, nil, fmt.Errorf("sync: creating a scratch directory: %w", err)
	}
	defer os.RemoveAll(tmp)

	label := runLabel(r)
	dlArgs := []string{"run", "download", strconv.FormatInt(r.DatabaseID, 10), "--repo", repo, "--dir", tmp}
	if out, err := exec.Command("gh", dlArgs...).CombinedOutput(); err != nil {
		return nil, []SkipReason{{
			Artifact: label,
			Reason:   fmt.Sprintf("gh run download failed: %v: %s", err, strings.TrimSpace(string(out))),
		}}, nil
	}

	manifests, err := findManifests(tmp)
	if err != nil {
		return nil, nil, err
	}
	webReplays, err := findWebReplayBundles(tmp)
	if err != nil {
		return nil, nil, err
	}
	if len(manifests) == 0 && len(webReplays) == 0 {
		return nil, []SkipReason{{
			Artifact: label,
			Reason:   "no <app>/<flow>/<run-id>/manifest.json and no pixel-replay shots found anywhere in the downloaded artifact(s)",
		}}, nil
	}

	var actor string
	var actorFetched bool
	fetchActorOnce := func() error {
		if actorFetched {
			return nil
		}
		var ferr error
		actor, ferr = fetchActor(repo, r.DatabaseID)
		actorFetched = true
		return ferr
	}

	for _, m := range manifests {
		// A manifest.json must PARSE as a retrace manifest to be treated as
		// a run. A Maestro debug bundle (uploaded by a mobile E2E workflow)
		// carries its own manifest.json at maestro-schemas/artifact-manifest —
		// findManifests can't tell them apart by name, and deriving
		// app/flow/run-id from its path yields a junk `tests/<timestamp>/<cell>`
		// app that pollutes every consumer. runs.ReadManifest rejects a
		// non-retrace schema, so this is the same guard appIsReal applies at
		// the queue, moved up to ingest so the junk never lands on disk.
		if _, rerr := runs.ReadManifest(m); rerr != nil {
			skipped = append(skipped, SkipReason{
				Artifact: label,
				Reason:   fmt.Sprintf("not a retrace manifest (schema check failed), skipping %s: %v", m, rerr),
			})
			continue
		}
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
		if !o.appAllowed(app) {
			skipped = append(skipped, SkipReason{
				Artifact: label,
				Reason:   fmt.Sprintf("%s: not in this sync's app allowlist", app),
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
		if err := fetchActorOnce(); err != nil {
			return synced, skipped, err
		}
		if err := runs.WriteSource(runs.Paths{RunDir: dest}, runs.Source{
			Kind: runs.SourceKindCI, Workflow: r.WorkflowName, RunURL: r.URL, SHA: r.HeadSHA,
			HeadBranch: r.HeadBranch, Event: r.Event, Actor: actor, SyncedAt: o.now(),
		}); err != nil {
			return synced, skipped, fmt.Errorf("sync: writing source.json for %s/%s/%s: %w", app, flow, runID, err)
		}
		synced = append(synced, filepath.Join(app, flow, runID))
	}

	// Pixel-only replay bundles (e.g. a Playwright `retrace replay`
	// artifact) never write their own manifest.json — synthesize one here
	// and finalize the run, so `retrace runs` reports it complete rather
	// than abandoned.
	for _, b := range webReplays {
		if !o.appAllowed(b.app) {
			skipped = append(skipped, SkipReason{
				Artifact: label,
				Reason:   fmt.Sprintf("%s: not in this sync's app allowlist", b.app),
			})
			continue
		}
		paths, perr := runs.PathsFor(runs.RunsRoot(o.Cwd), b.app, b.flow, b.runID)
		if perr != nil {
			skipped = append(skipped, SkipReason{
				Artifact: label,
				Reason:   fmt.Sprintf("pixel replay at an unusable path (need <app>/<flow>/<run-id>/shots): %v", perr),
			})
			continue
		}
		if _, statErr := os.Stat(paths.RunDir); statErr == nil {
			continue // already synced — same idempotency check as the manifest loop
		}

		manifest, merr := synthesizeReplayManifest(b)
		if merr != nil {
			return synced, skipped, fmt.Errorf("sync: reading shots for %s/%s/%s: %w", b.app, b.flow, b.runID, merr)
		}
		if err := copyTree(b.runDir, paths.RunDir); err != nil {
			return synced, skipped, fmt.Errorf("sync: copying %s/%s/%s: %w", b.app, b.flow, b.runID, err)
		}
		if err := runs.WriteManifest(paths, &manifest); err != nil {
			return synced, skipped, fmt.Errorf("sync: writing synthesized manifest for %s/%s/%s: %w", b.app, b.flow, b.runID, err)
		}
		if err := fetchActorOnce(); err != nil {
			return synced, skipped, err
		}
		if err := runs.WriteSource(paths, runs.Source{
			Kind: runs.SourceKindCI, Workflow: r.WorkflowName, RunURL: r.URL, SHA: r.HeadSHA,
			HeadBranch: r.HeadBranch, Event: r.Event, Actor: actor, SyncedAt: o.now(),
		}); err != nil {
			return synced, skipped, fmt.Errorf("sync: writing source.json for %s/%s/%s: %w", b.app, b.flow, b.runID, err)
		}
		if err := runs.Finalize(paths, runs.Finalized{RunID: b.runID, FinishedAt: o.now()}); err != nil {
			return synced, skipped, fmt.Errorf("sync: finalizing %s/%s/%s: %w", b.app, b.flow, b.runID, err)
		}
		synced = append(synced, filepath.Join(b.app, b.flow, b.runID))
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
