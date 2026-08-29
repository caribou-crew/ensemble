package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/caribou-crew/ensemble/retrace/sync"
)

const syncUsage = `retrace sync — pull CI-recorded runs into .retrace/runs

Usage:
  retrace sync --from github --repo ORG/REPO [--repos A,B] [--workflow NAME] [--workflows PATTERN,PATTERN] [--branch NAME] [--actor USER] [--event EVENT] [--status STATUS] [--since 7d] [--dry-run] [--json]
  retrace sync list --repo ORG/REPO [--repos A,B] [--workflow NAME] [--workflows PATTERN,PATTERN] [--branch NAME] [--actor USER] [--event EVENT] [--status STATUS] [--since 7d] [--json]

Only --from github is implemented today. Auth is whatever "gh" itself
resolves (gh auth login, or GH_TOKEN/GITHUB_TOKEN) — this command never
handles a token directly.

--repos/--workflows accept a comma-separated list; each --workflows entry
may be an exact name or a glob (e.g. "Retrace *"). --dry-run reports what
"sync list" would show for the same filters instead of pulling anything.

"list" discovers candidate runs — branch, actor, event, status, and
whether GitHub reports any artifact at all — without downloading anything.
`

// cmdSync is the CLI face of sync.Run — see that package's doc comment for
// why this delegates entirely rather than re-implementing anything: the
// CLI and ensemble's `POST /api/retrace/sync` route call the exact same
// function.
func cmdSync(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "list" {
		return cmdSyncList(args[1:], stdout, stderr)
	}

	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	fs.SetOutput(stderr)
	from := fs.String("from", "github", "sync source: \"github\" (only supported value today)")
	repo := fs.String("repo", "", "GitHub repo as ORG/REPO (required unless --repos is set)")
	repos := fs.String("repos", "", "comma-separated GitHub repos, each ORG/REPO")
	workflow := fs.String("workflow", "", "limit sync to one GitHub Actions workflow name")
	workflows := fs.String("workflows", "", "comma-separated workflow names/globs")
	branch := fs.String("branch", "", "limit sync to runs off this branch")
	actor := fs.String("actor", "", "limit sync to runs triggered by this GitHub user")
	event := fs.String("event", "", "limit sync to runs triggered by this event (e.g. push, schedule)")
	status := fs.String("status", "", "limit sync by run status or conclusion (e.g. completed, failure)")
	since := fs.String("since", "7d", "how far back to look for qualifying workflow runs")
	dryRun := fs.Bool("dry-run", false, "report what would be synced without downloading anything")
	asJSON := fs.Bool("json", false, "emit the result as JSON on stdout")
	fs.Usage = func() { fmt.Fprint(stderr, syncUsage) }
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if strings.TrimSpace(*repo) == "" && strings.TrimSpace(*repos) == "" {
		return fail(stderr, "sync: --repo or --repos is required")
	}
	sinceDur, err := sync.ParseSince(*since)
	if err != nil {
		return fail(stderr, "sync: %v", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return fail(stderr, "sync: cannot determine the working directory: %v", err)
	}

	o := sync.Options{
		Cwd: cwd, From: *from, Repo: *repo, Repos: splitCSV(*repos),
		Workflow: *workflow, Workflows: splitCSV(*workflows),
		Branch: *branch, Actor: *actor, Event: *event, Status: *status,
		Since: sinceDur,
	}

	if *dryRun {
		candidates, err := sync.List(o)
		if err != nil {
			return fail(stderr, "sync: %v", err)
		}
		if *asJSON {
			if err := writeJSON(stdout, candidates); err != nil {
				return fail(stderr, "sync: writing JSON: %v", err)
			}
			return exitOK
		}
		printSyncCandidates(stdout, candidates, "would sync")
		return exitOK
	}

	res, err := sync.Run(o)
	if err != nil {
		return fail(stderr, "sync: %v", err)
	}

	if *asJSON {
		if err := writeJSON(stdout, res); err != nil {
			return fail(stderr, "sync: writing JSON: %v", err)
		}
		return exitOK
	}
	if len(res.Synced) == 0 {
		fmt.Fprintln(stdout, "nothing new to sync")
	}
	for _, r := range res.Synced {
		fmt.Fprintf(stdout, "synced %s\n", r)
	}
	for _, sk := range res.Skipped {
		fmt.Fprintf(stdout, "skipped %s: %s\n", sk.Artifact, sk.Reason)
	}
	return exitOK
}

// splitCSV splits a comma-separated flag value into a trimmed, non-empty
// slice — "" becomes nil (not [""]), so an unset flag reads the same as
// one never passed.
func splitCSV(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// printSyncCandidates renders List's output as plain text — verb is
// "would sync" for --dry-run and "found" for `sync list`.
func printSyncCandidates(w io.Writer, candidates []sync.Candidate, verb string) {
	if len(candidates) == 0 {
		fmt.Fprintln(w, "no candidate runs in range")
		return
	}
	fmt.Fprintf(w, "%s %d candidate run(s):\n", verb, len(candidates))
	for _, c := range candidates {
		artifacts := "no artifacts"
		if c.HasArtifacts {
			artifacts = "has artifacts"
		}
		fmt.Fprintf(w, "  %d  %-12s %-30s %-20s %-10s %-12s %s  %s\n",
			c.DatabaseID, c.Status, c.WorkflowName, c.HeadBranch, c.Event, c.Actor, artifacts, c.CreatedAt.Format("2006-01-02T15:04:05Z"))
	}
}

// cmdSyncList is `retrace sync list` — the discovery half of discover →
// filter → select → pull. It shares every filter flag `retrace sync`
// itself takes except --dry-run (list already never downloads).
func cmdSyncList(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("sync list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	from := fs.String("from", "github", "sync source: \"github\" (only supported value today)")
	repo := fs.String("repo", "", "GitHub repo as ORG/REPO (required unless --repos is set)")
	repos := fs.String("repos", "", "comma-separated GitHub repos, each ORG/REPO")
	workflow := fs.String("workflow", "", "limit to one GitHub Actions workflow name")
	workflows := fs.String("workflows", "", "comma-separated workflow names/globs")
	branch := fs.String("branch", "", "limit to runs off this branch")
	actor := fs.String("actor", "", "limit to runs triggered by this GitHub user")
	event := fs.String("event", "", "limit to runs triggered by this event (e.g. push, schedule)")
	status := fs.String("status", "", "limit by run status or conclusion (e.g. completed, failure)")
	since := fs.String("since", "7d", "how far back to look for qualifying workflow runs")
	asJSON := fs.Bool("json", false, "emit the candidates as JSON on stdout")
	fs.Usage = func() { fmt.Fprint(stderr, syncUsage) }
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if strings.TrimSpace(*repo) == "" && strings.TrimSpace(*repos) == "" {
		return fail(stderr, "sync list: --repo or --repos is required")
	}
	sinceDur, err := sync.ParseSince(*since)
	if err != nil {
		return fail(stderr, "sync list: %v", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return fail(stderr, "sync list: cannot determine the working directory: %v", err)
	}

	candidates, err := sync.List(sync.Options{
		Cwd: cwd, From: *from, Repo: *repo, Repos: splitCSV(*repos),
		Workflow: *workflow, Workflows: splitCSV(*workflows),
		Branch: *branch, Actor: *actor, Event: *event, Status: *status,
		Since: sinceDur,
	})
	if err != nil {
		return fail(stderr, "sync list: %v", err)
	}

	if *asJSON {
		if err := writeJSON(stdout, candidates); err != nil {
			return fail(stderr, "sync list: writing JSON: %v", err)
		}
		return exitOK
	}
	printSyncCandidates(stdout, candidates, "found")
	return exitOK
}
