package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/caribou-crew/ensemble/retrace/config"
	"github.com/caribou-crew/ensemble/retrace/diff"
	"github.com/caribou-crew/ensemble/retrace/refs"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

// cmdDiff compares two runs of the same flow and reports the unified
// Summary — the CLI face of diff.Build, which is the one place every field
// of that document gets set. See diff/summary.go's package doc.
func cmdDiff(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("diff", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		flow = fs.String("flow", "", "flow name to diff (required)")
		app  = fs.String("app", "", "app name (default: config app, else the directory name)")
		// aSel defaults to "reference" — the accepted baseline bundle, or
		// the newest run eligible to stand in for one. See resolveSide.
		aSel          = fs.String("a", "reference", "side A selector: \"reference\", \"latest\", an exact run id, or a git sha prefix")
		bSel          = fs.String("b", "latest", "side B selector: \"latest\", an exact run id, or a git sha prefix")
		asJSON        = fs.Bool("json", false, "emit the Summary as JSON on stdout")
		images        = fs.Bool("images", true, "write diff/overlay checkpoint images")
		out           = fs.String("out", "", "where diff/overlay images are written (default: side B's run directory)")
		allowDegraded = fs.Bool("allow-degraded", false, "compare even when a side's capture-trust verdict is not ok, instead of quarantining")
		noFail        = fs.Bool("no-fail", false, "compute and report every gate/budget as usual, but always exit 0")
		requireWhy    = fs.Bool("require-why", false, "refuse to run when any tolerance in the config carries no `why:`")
	)
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if strings.TrimSpace(*flow) == "" {
		return fail(stderr, "diff: --flow is required")
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fail(stderr, "diff: cannot determine the working directory: %v", err)
	}
	cfg, err := config.Discover(cwd)
	if err != nil {
		return fail(stderr, "%v", err)
	}
	// The flag only ever turns the ratchet ON. It cannot turn off a
	// `require_why: true` already in the config — a CLI flag that could
	// switch off a project's own rule is a bypass, and the whole point of
	// the setting is that it cannot be bypassed by the person it inconveniences.
	if *requireWhy {
		if err := cfg.ValidateWhy(); err != nil {
			return fail(stderr, "%v", err)
		}
	}

	appName := *app
	if appName == "" {
		appName = cfg.App
	}
	if appName == "" {
		appName = filepath.Base(cwd)
	}

	a, err := resolveSide(cwd, appName, *flow, *aSel)
	if err != nil {
		return fail(stderr, "diff: %v", err)
	}
	// "none" means "I could not compare", NEVER "nothing differed". Exit 3
	// (could not evaluate), naming the verb that fixes it — never a diff
	// against an empty directory, which would report every call as missing.
	if a.Kind == "none" {
		return fail(stderr, "diff: no reference bundle for side a: %s\nrun `retrace ref accept --flow %s` once this flow has a good run", noneReason(cwd, appName, *flow), *flow)
	}
	b, err := resolveSide(cwd, appName, *flow, *bSel)
	if err != nil {
		return fail(stderr, "diff: %v", err)
	}
	if b.Kind == "none" {
		return fail(stderr, "diff: no reference bundle for side b: %s\nrun `retrace ref accept --flow %s` once this flow has a good run", noneReason(cwd, appName, *flow), *flow)
	}

	opts, err := diff.OptionsFor(cfg, a.Manifest, b.Manifest)
	if err != nil {
		return fail(stderr, "diff: %v", err)
	}

	outDir := *out
	if outDir == "" {
		outDir = b.Dir
	}

	s, err := diff.Build(diff.BuildInput{
		App: appName, Flow: *flow, A: a, B: b, Cfg: cfg,
		Options: opts, WantImages: *images, OutDir: outDir,
		AllowDegraded: *allowDegraded,
	})
	if err != nil {
		return fail(stderr, "diff: %v", err)
	}

	if *asJSON {
		if err := writeJSON(stdout, s); err != nil {
			return fail(stderr, "diff: %v", err)
		}
	} else {
		diff.RenderText(stdout, s)
	}

	// --no-fail is applied LAST, at this call only: it overrides the code
	// ExitCode(s) returns, it does not change s or ExitCode itself, so
	// --json output is identical with or without it — a reporting run must
	// not also blind the reader to what it found.
	//
	// It suppresses FINDINGS, not INABILITY TO RUN. "changed" and "failed"
	// are findings, and a report-only CI job legitimately does not want the
	// build broken by them. "quarantined" is not a finding: nothing was
	// compared, so there is nothing to report as clean, and forcing 0 there
	// would let a job announce success for a run that was never evaluated —
	// the zero-value trap wearing a command-line flag. The same class of
	// "could not evaluate" already exits 3 through fail() above (a bad
	// flag, an unreadable config, an I/O failure) whether or not the flag
	// was passed, so exempting quarantine also makes the two consistent.
	code := diff.ExitCode(s)
	if *noFail {
		switch s.Verdict {
		case "changed", "failed":
			code = exitOK
		}
	}
	return code
}

// resolveSide resolves one --a/--b selector to a RunRef.
//
// "reference" goes through refs.Resolve — the committed bundle, else the
// newest eligible run, else nothing with a reason. The returned Reference
// maps onto a RunRef by copying Kind straight through: both use
// "bundle" | "run" | "none", one vocabulary, nothing to translate.
func resolveSide(cwd, app, flow, selector string) (diff.RunRef, error) {
	root := runs.RunsRoot(cwd)
	if selector == "reference" {
		r := refs.Resolve(cwd, root, app, flow)
		return diff.RunRef{RunID: r.RunID, Kind: r.Kind, Dir: r.Dir, Manifest: r.Manifest}, nil
	}
	id := runs.FindRun(root, app, flow, selector)
	if id == "" {
		return diff.RunRef{}, fmt.Errorf("no run matches %q for %s/%s", selector, app, flow)
	}
	p, err := runs.PathsFor(root, app, flow, id)
	if err != nil {
		return diff.RunRef{}, err
	}
	m, err := runs.ReadManifest(p.ManifestPath)
	if err != nil {
		return diff.RunRef{}, fmt.Errorf("reading manifest for %s/%s/%s: %w", app, flow, id, err)
	}
	return diff.RunRef{RunID: id, Kind: "run", Dir: p.RunDir, Manifest: m}, nil
}

// noneReason re-asks refs.Resolve for the explanation behind a "none", so
// the refusal names the runs it tried instead of only saying there is no
// reference. Resolving twice is cheap (a few Stats and one JSON decode) and
// keeps resolveSide's return type the RunRef every other caller wants.
func noneReason(cwd, app, flow string) string {
	r := refs.Resolve(cwd, runs.RunsRoot(cwd), app, flow)
	if r.Reason == "" {
		return "no reference resolved"
	}
	return r.Reason
}
