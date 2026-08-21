package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/retrace/config"
	"github.com/caribou-crew/ensemble/retrace/diff"
	"github.com/caribou-crew/ensemble/retrace/diff/pixel"
	"github.com/caribou-crew/ensemble/retrace/refs"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

const refUsage = `retrace ref — the reference bundle a diff runs against

Usage:
  retrace ref list   [--app NAME] [--flow NAME] [--json]
  retrace ref accept --flow NAME [--app NAME] [--run SELECTOR] [--json]
  retrace ref reject --flow NAME [--app NAME] [--run SELECTOR] [--out DIR] [--json]
`

// cmdRef dispatches the `ref` verbs. Every one of them is also a REST call
// an agent can make identically (Task 13) — this is the CLI face, never a
// second implementation.
func cmdRef(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, refUsage)
		return exitUsage
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Fprint(stdout, refUsage)
		return exitOK
	case "list":
		return cmdRefList(args[1:], stdout, stderr)
	case "accept":
		return cmdRefAccept(args[1:], stdout, stderr)
	case "reject":
		return cmdRefReject(args[1:], stdout, stderr)
	default:
		return fail(stderr, "unknown ref verb %q\n\n%s", args[0], refUsage)
	}
}

// project is the working context every ref verb needs: where we are, what
// the config says, and which app that resolves to. Resolved in ONE place so
// `ref` and `diff` cannot disagree about which app a bare invocation means.
type project struct {
	cwd      string
	cfg      *config.Config
	app      string
	runsRoot string
}

func resolveProject(appFlag string) (project, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return project{}, fmt.Errorf("cannot determine the working directory: %w", err)
	}
	cfg, err := config.Discover(cwd)
	if err != nil {
		return project{}, err
	}
	app := appFlag
	if app == "" {
		app = cfg.App
	}
	if app == "" {
		app = filepath.Base(cwd)
	}
	return project{cwd: cwd, cfg: cfg, app: app, runsRoot: runs.RunsRoot(cwd)}, nil
}

// masksFor is the ONE place config.Rect becomes pixel.Rect for a promotion.
// The conversion is pixel.RectsFrom and lives at this boundary; refs never
// imports retrace/config, and nothing converts a second time.
func (p project) masksFor(flow string) func(string) []pixel.Rect {
	return func(checkpoint string) []pixel.Rect {
		return pixel.RectsFrom(p.cfg.MasksFor(flow, checkpoint))
	}
}

// flowsToList is every flow that HAS something to report: one that has
// recorded runs, one that has a committed bundle, or one the config
// declares. A flow declared but never run still gets a line, because "you
// configured this and have never recorded it" is exactly what an operator
// running `ref list` is trying to find out.
func flowsToList(p project, flowFlag string) []string {
	if flowFlag != "" {
		return []string{flowFlag}
	}
	seen := map[string]bool{}
	for _, f := range runs.ListFlows(p.runsRoot, p.app) {
		seen[f] = true
	}
	for _, f := range runs.ListFlows(runs.RefsRoot(p.cwd), p.app) {
		seen[f] = true
	}
	for f := range p.cfg.Flows {
		seen[f] = true
	}
	out := make([]string, 0, len(seen))
	for f := range seen {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// refListing is one flow's answer, in the shape `--json` emits.
type refListing struct {
	App       string         `json:"app"`
	Flow      string         `json:"flow"`
	Reference refs.Reference `json:"reference"`
}

func cmdRefList(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ref list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	app := fs.String("app", "", "app name (default: config app, else the directory name)")
	flow := fs.String("flow", "", "limit to one flow")
	asJSON := fs.Bool("json", false, "emit the listing as JSON on stdout")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	p, err := resolveProject(*app)
	if err != nil {
		return fail(stderr, "ref list: %v", err)
	}

	flows := flowsToList(p, *flow)
	out := make([]refListing, 0, len(flows))
	for _, f := range flows {
		out = append(out, refListing{App: p.app, Flow: f, Reference: refs.Resolve(p.cwd, p.runsRoot, p.app, f)})
	}
	if *asJSON {
		if err := writeJSON(stdout, out); err != nil {
			return fail(stderr, "ref list: %v", err)
		}
		return exitOK
	}
	if len(out) == 0 {
		fmt.Fprintf(stdout, "no flows recorded or configured for %s\n", p.app)
		return exitOK
	}
	for _, e := range out {
		switch e.Reference.Kind {
		case "bundle":
			fmt.Fprintf(stdout, "%s/%s  bundle  %s  (from run %s)\n", e.App, e.Flow, e.Reference.Dir, e.Reference.RunID)
		case "run":
			fmt.Fprintf(stdout, "%s/%s  run     %s  (no committed bundle; `retrace ref accept --flow %s` to promote it)\n",
				e.App, e.Flow, e.Reference.RunID, e.Flow)
		default:
			fmt.Fprintf(stdout, "%s/%s  none    %s\n", e.App, e.Flow, e.Reference.Reason)
			// Every candidate, not a summary line: an empty state that says
			// only "no reference" is useless, and one that says "no
			// candidates" when there were five dirty runs is worse.
			for _, c := range e.Reference.History {
				detail := ""
				if c.Detail != "" {
					detail = " — " + c.Detail
				}
				fmt.Fprintf(stdout, "    %s  %s%s\n", c.RunID, c.Reason, detail)
			}
		}
	}
	return exitOK
}

func cmdRefAccept(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ref accept", flag.ContinueOnError)
	fs.SetOutput(stderr)
	flow := fs.String("flow", "", "flow name to promote (required)")
	app := fs.String("app", "", "app name (default: config app, else the directory name)")
	sel := fs.String("run", "latest", "which run to promote: \"latest\", an exact run id, or a git sha prefix")
	asJSON := fs.Bool("json", false, "emit the result as JSON on stdout")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if strings.TrimSpace(*flow) == "" {
		return fail(stderr, "ref accept: --flow is required")
	}
	p, err := resolveProject(*app)
	if err != nil {
		return fail(stderr, "ref accept: %v", err)
	}
	runID := runs.FindRun(p.runsRoot, p.app, *flow, *sel)
	if runID == "" {
		return fail(stderr, "ref accept: no run matches %q for %s/%s", *sel, p.app, *flow)
	}

	res, err := refs.Accept(refs.AcceptOptions{
		Cwd: p.cwd, RunsRoot: p.runsRoot, App: p.app, Flow: *flow, RunID: runID,
		MasksFor: p.masksFor(*flow),
	})
	if err != nil {
		return fail(stderr, "ref accept: %v", err)
	}
	// The warning goes to STDERR, not stdout: stdout carries the --json
	// contract, and stderr is the channel a CI log keeps and a human sees.
	// AcceptResult.CaptureStatus carries the same fact as a typed value, so
	// nothing depends on parsing this sentence.
	if res.CaptureStatus != trace.VerdictOK {
		fmt.Fprintf(stderr, "retrace: warning: promoting %s, whose capture verdict is %q — every diff against this reference now inherits that doubt; re-record and re-accept if it was a proxy problem rather than a real change\n",
			runID, res.CaptureStatus)
	}
	if *asJSON {
		if err := writeJSON(stdout, res); err != nil {
			return fail(stderr, "ref accept: %v", err)
		}
		return exitOK
	}
	fmt.Fprintf(stdout, "accepted %s as the reference for %s/%s\n  %s\n  %d files, %d bytes, capture %s\n",
		res.RunID, p.app, *flow, res.Dir, len(res.Files), res.Bytes, res.CaptureStatus)
	return exitOK
}

func cmdRefReject(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ref reject", flag.ContinueOnError)
	fs.SetOutput(stderr)
	flow := fs.String("flow", "", "flow name (required)")
	app := fs.String("app", "", "app name (default: config app, else the directory name)")
	sel := fs.String("run", "latest", "which run to capture: \"latest\", an exact run id, or a git sha prefix")
	out := fs.String("out", "", "where the repro bundle is written (default: .retrace/repro)")
	asJSON := fs.Bool("json", false, "emit the result as JSON on stdout")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if strings.TrimSpace(*flow) == "" {
		return fail(stderr, "ref reject: --flow is required")
	}
	p, err := resolveProject(*app)
	if err != nil {
		return fail(stderr, "ref reject: %v", err)
	}
	runID := runs.FindRun(p.runsRoot, p.app, *flow, *sel)
	if runID == "" {
		return fail(stderr, "ref reject: no run matches %q for %s/%s", *sel, p.app, *flow)
	}

	// The summary is best-effort: a repro bundle is worth having even when
	// the diff that would explain it cannot be computed (no reference yet, a
	// spec that fails to load). What is NOT acceptable is writing an empty
	// summary.json, which would assert a comparison that never ran — refs
	// omits the file entirely for a nil Summary.
	summary, why := rejectSummary(p, *flow, runID)
	if summary == nil {
		fmt.Fprintf(stderr, "retrace: warning: no summary.json in this repro bundle — %s\n", why)
	}
	res, err := refs.Reject(refs.RejectOptions{
		Cwd: p.cwd, RunsRoot: p.runsRoot, App: p.app, Flow: *flow, RunID: runID,
		OutDir: *out, Summary: summary,
	})
	if err != nil {
		return fail(stderr, "ref reject: %v", err)
	}
	if *asJSON {
		if err := writeJSON(stdout, res); err != nil {
			return fail(stderr, "ref reject: %v", err)
		}
		return exitOK
	}
	fmt.Fprintf(stdout, "wrote a repro bundle for %s/%s/%s\n  %s\n  %d files\n", p.app, *flow, runID, res.Dir, len(res.Files))
	return exitOK
}

// rejectSummary diffs the rejected run against whatever the flow currently
// resolves to. It returns (nil, why) rather than an error: the caller wants
// the bundle either way, and "why" is what stderr says instead.
func rejectSummary(p project, flow, runID string) (*diff.Summary, string) {
	a, err := resolveSide(p.cwd, p.app, flow, "reference")
	if err != nil {
		return nil, fmt.Sprintf("side A did not resolve: %v", err)
	}
	if a.Kind == "none" {
		return nil, "there is no reference to compare against yet; run `retrace ref accept` once this flow has a good run"
	}
	b, err := resolveSide(p.cwd, p.app, flow, runID)
	if err != nil {
		return nil, fmt.Sprintf("side B did not resolve: %v", err)
	}
	opts, err := diff.OptionsFor(p.cfg, a.Manifest, b.Manifest)
	if err != nil {
		return nil, fmt.Sprintf("the diff options could not be assembled: %v", err)
	}
	s, err := diff.Build(diff.BuildInput{
		App: p.app, Flow: flow, A: a, B: b, Cfg: p.cfg, Options: opts,
		// No images: a repro bundle is about the record, and writing diff
		// PNGs would mutate the run directory being captured.
		WantImages: false, OutDir: b.Dir,
	})
	if err != nil {
		return nil, fmt.Sprintf("the diff could not be computed: %v", err)
	}
	return &s, ""
}
