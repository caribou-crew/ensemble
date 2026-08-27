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
  retrace sync --from github --repo ORG/REPO [--workflow NAME] [--since 7d] [--json]

Only --from github is implemented today. Auth is whatever "gh" itself
resolves (gh auth login, or GH_TOKEN/GITHUB_TOKEN) — this command never
handles a token directly.
`

// cmdSync is the CLI face of sync.Run — see that package's doc comment for
// why this delegates entirely rather than re-implementing anything: the
// CLI and ensemble's `POST /api/retrace/sync` route call the exact same
// function.
func cmdSync(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	fs.SetOutput(stderr)
	from := fs.String("from", "github", "sync source: \"github\" (only supported value today)")
	repo := fs.String("repo", "", "GitHub repo as ORG/REPO (required for --from github)")
	workflow := fs.String("workflow", "", "limit sync to one GitHub Actions workflow name")
	since := fs.String("since", "7d", "how far back to look for qualifying workflow runs")
	asJSON := fs.Bool("json", false, "emit the result as JSON on stdout")
	fs.Usage = func() { fmt.Fprint(stderr, syncUsage) }
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if strings.TrimSpace(*repo) == "" {
		return fail(stderr, "sync: --repo is required")
	}
	sinceDur, err := sync.ParseSince(*since)
	if err != nil {
		return fail(stderr, "sync: %v", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return fail(stderr, "sync: cannot determine the working directory: %v", err)
	}

	res, err := sync.Run(sync.Options{Cwd: cwd, From: *from, Repo: *repo, Workflow: *workflow, Since: sinceDur})
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
	for _, s := range res.Skipped {
		fmt.Fprintf(stdout, "skipped %s: %s\n", s.Artifact, s.Reason)
	}
	return exitOK
}
