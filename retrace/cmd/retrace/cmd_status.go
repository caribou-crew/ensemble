package main

// `retrace status` — the headless readout an agent (or a CI step) reads to
// learn whether every flow is passing, without opening the dashboard.
//
//	retrace status              # verdict per flow from what's already synced
//	retrace status --sync       # pull the latest CI runs first, then read out
//	retrace status --json       # machine-readable, one object per flow + summary
//
// Exit code is the WORST verdict across all flows, mirroring diff's CI
// contract so a script can branch on it directly:
//
//	0  every flow passed
//	1  at least one flow CHANGED (differs from its committed reference)
//	2  at least one hard GATE failed
//	3  at least one flow could not be evaluated (quarantined / no reference),
//	   or a setup error (unreadable config, sync failure)
//
// Repo + sync filters come from retrace.repo.yaml (repo:, sync:) so an agent
// runs a bare `retrace status --sync` with no flags; --repo overrides it.

import (
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/caribou-crew/ensemble/retrace/repoconfig"
	"github.com/caribou-crew/ensemble/retrace/serve"
	"github.com/caribou-crew/ensemble/retrace/sync"
)

const statusUsage = `retrace status — verdict per flow, for an agent or CI step (no dashboard)

  retrace status [--sync] [--json] [--repo ORG/REPO] [--since 7d]

  --sync   pull the latest CI runs from GitHub first (repo/filters from
           retrace.repo.yaml unless --repo/--since override)
  --json   emit one object per flow plus a summary, instead of text
  --repo   GitHub repo as ORG/REPO (default: retrace.repo.yaml's repo:)
  --since  how far back --sync looks (default: retrace.repo.yaml sync.since, else 7d)

Exit code is the worst verdict: 0 pass, 1 changed, 2 gate failed, 3 not
evaluable / setup error.`

// statusFlow is one flow's line in the --json readout.
type statusFlow struct {
	App     string   `json:"app"`
	Flow    string   `json:"flow"`
	Verdict string   `json:"verdict"` // pass | changed | failed | quarantined
	RunID   string   `json:"runId,omitempty"`
	Gates   []string `json:"gates,omitempty"`
}

// statusReport is the --json document.
type statusReport struct {
	Synced   []string     `json:"synced,omitempty"`
	Flows    []statusFlow `json:"flows"`
	Summary  statusCounts `json:"summary"`
	ExitCode int          `json:"exitCode"`
}

type statusCounts struct {
	Pass        int `json:"pass"`
	Changed     int `json:"changed"`
	Failed      int `json:"failed"`
	NotCompared int `json:"notCompared"`
}

func cmdStatus(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	doSync := fs.Bool("sync", false, "pull latest CI runs before reading verdicts")
	asJSON := fs.Bool("json", false, "emit JSON")
	repoFlag := fs.String("repo", "", "GitHub repo ORG/REPO (default: retrace.repo.yaml)")
	sinceFlag := fs.String("since", "", "how far back --sync looks (default: retrace.repo.yaml sync.since, else 7d)")
	fs.Usage = func() { fmt.Fprintln(stderr, statusUsage) }
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fail(stderr, "status: cannot determine the working directory: %v", err)
	}

	repoCfg, _, _ := repoconfig.Discover(cwd)

	// --sync: pull latest first, repo/filters from repo.yaml unless overridden.
	var synced []string
	if *doSync {
		repo := *repoFlag
		if repo == "" && repoCfg != nil {
			repo = repoCfg.Repo
		}
		if repo == "" {
			return fail(stderr, "status --sync: no repo — set retrace.repo.yaml `repo:` or pass --repo ORG/REPO")
		}
		sinceStr := *sinceFlag
		if sinceStr == "" && repoCfg != nil {
			sinceStr = repoCfg.Sync.Since
		}
		if sinceStr == "" {
			sinceStr = "7d"
		}
		sinceDur, perr := sync.ParseSince(sinceStr)
		if perr != nil {
			return fail(stderr, "status --sync: %v", perr)
		}
		o := sync.Options{Cwd: cwd, From: "github", Repo: repo, Since: sinceDur}
		if repoCfg != nil {
			o.Workflows = repoCfg.Sync.Workflows
			o.Branch, o.Actor, o.Event, o.Status = repoCfg.Sync.Branch, repoCfg.Sync.Actor, repoCfg.Sync.Event, repoCfg.Sync.Status
		}
		res, serr := sync.Run(o)
		if serr != nil {
			return fail(stderr, "status --sync: %v", serr)
		}
		synced = res.Synced
	}

	// Build the queue — the same verdicts the dashboard shows — across every
	// root the repo.yaml declares (or the single cwd root when there is none).
	items, err := statusQueue(cwd, repoCfg)
	if err != nil {
		return fail(stderr, "status: %v", err)
	}
	// One row per app/flow, latest run wins — a repo.yaml that aggregates
	// two roots can emit the same flow twice (the dashboard dedupes the same
	// way). Without this the readout double-counts and an agent misreads the
	// summary.
	items = dedupeItems(items)

	flows := make([]statusFlow, 0, len(items))
	var counts statusCounts
	worst := exitOK
	for _, it := range items {
		flows = append(flows, statusFlow{App: it.App, Flow: it.Flow, Verdict: it.Verdict, RunID: it.RunID, Gates: it.Gates})
		switch it.Verdict {
		case "pass":
			counts.Pass++
		case "changed":
			counts.Changed++
			if worst < exitDiff {
				worst = exitDiff
			}
		case "failed":
			counts.Failed++
			if worst < exitGate {
				worst = exitGate
			}
		case "quarantined":
			counts.NotCompared++
			worst = exitUsage // 3: could not evaluate
		}
	}
	sort.Slice(flows, func(i, j int) bool {
		if flows[i].App != flows[j].App {
			return flows[i].App < flows[j].App
		}
		return flows[i].Flow < flows[j].Flow
	})

	if *asJSON {
		if err := writeJSON(stdout, statusReport{Synced: synced, Flows: flows, Summary: counts, ExitCode: worst}); err != nil {
			return fail(stderr, "status: writing JSON: %v", err)
		}
		return worst
	}

	if len(synced) > 0 {
		fmt.Fprintf(stdout, "synced %d run(s) from CI\n", len(synced))
	}
	if len(flows) == 0 {
		fmt.Fprintln(stdout, "no flows found — nothing recorded or synced yet")
		return worst
	}
	for _, f := range flows {
		label := f.Verdict
		if label == "quarantined" {
			label = "not compared"
		}
		fmt.Fprintf(stdout, "%-24s %-16s %s\n", f.App, f.Flow, strings.ToUpper(label))
		for _, g := range f.Gates {
			fmt.Fprintf(stdout, "    · %s\n", g)
		}
	}
	fmt.Fprintf(stdout, "\n%d pass · %d changed · %d failed · %d not-compared\n",
		counts.Pass, counts.Changed, counts.Failed, counts.NotCompared)
	return worst
}

// dedupeItems keeps one item per app/flow — the one with the newest RunID
// (a sortable UTC-timestamp prefix) — so a multi-root aggregation does not
// list the same flow twice. Mirrors the dashboard's dedupeByKey.
func dedupeItems(items []serve.Item) []serve.Item {
	latest := map[string]serve.Item{}
	order := []string{}
	for _, it := range items {
		k := it.App + "/" + it.Flow
		prev, seen := latest[k]
		if !seen {
			order = append(order, k)
			latest[k] = it
			continue
		}
		if it.RunID > prev.RunID {
			latest[k] = it
		}
	}
	out := make([]serve.Item, 0, len(order))
	for _, k := range order {
		out = append(out, latest[k])
	}
	return out
}

// statusQueue builds the verdict queue across every root the repo.yaml
// declares, or the single cwd root when there is no repo.yaml — the same
// resolution `retrace serve` uses, so the readout matches the dashboard.
func statusQueue(cwd string, repoCfg *repoconfig.Config) ([]serve.Item, error) {
	if repoCfg == nil {
		d, err := serve.NewDepsForRoot(cwd, nil, "")
		if err != nil {
			return nil, err
		}
		return serve.BuildQueue(d)
	}
	byRoot := make(map[string]serve.Deps, len(repoCfg.Apps))
	for _, root := range repoCfg.Roots() {
		d, err := serve.NewDepsForRoot(root, nil, "")
		if err != nil {
			return nil, err
		}
		byRoot[root] = d
	}
	appRoot := make(map[string]string, len(repoCfg.Apps))
	for app, entry := range repoCfg.Apps {
		appRoot[app] = entry.Root
	}
	sources, err := serve.NewSources(byRoot, appRoot)
	if err != nil {
		return nil, err
	}
	return sources.BuildQueue()
}
