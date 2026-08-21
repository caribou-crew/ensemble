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
		// aSel defaults to "reference" — the accepted baseline bundle. Until
		// Task 11 lands, that selector is a stub (see resolveSide) and
		// always errors; pass an explicit run selector (an exact run id, a
		// git sha prefix, or "latest") to compare two recorded runs today.
		aSel          = fs.String("a", "reference", "side A selector: \"reference\", \"latest\", an exact run id, or a git sha prefix")
		bSel          = fs.String("b", "latest", "side B selector: \"latest\", an exact run id, or a git sha prefix")
		asJSON        = fs.Bool("json", false, "emit the Summary as JSON on stdout")
		images        = fs.Bool("images", true, "write diff/overlay checkpoint images")
		out           = fs.String("out", "", "where diff/overlay images are written (default: side B's run directory)")
		allowDegraded = fs.Bool("allow-degraded", false, "compare even when a side's capture-trust verdict is not ok, instead of quarantining")
		noFail        = fs.Bool("no-fail", false, "compute and report every gate/budget as usual, but always exit 0")
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
	if a.Kind == "none" {
		return fail(stderr, "diff: no reference bundle: run `retrace ref accept` first")
	}
	b, err := resolveSide(cwd, appName, *flow, *bSel)
	if err != nil {
		return fail(stderr, "diff: %v", err)
	}
	if b.Kind == "none" {
		return fail(stderr, "diff: no reference bundle: run `retrace ref accept` first")
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
	code := diff.ExitCode(s)
	if *noFail {
		code = exitOK
	}
	return code
}

// resolveSide resolves one --a/--b selector to a RunRef.
//
// TODO(task-11): replace with refs.Resolve. Until then the default
// selector errors — see Task 11, which owns this line.
func resolveSide(cwd, app, flow, selector string) (diff.RunRef, error) {
	if selector == "reference" {
		return diff.RunRef{Kind: "none"}, nil
	}
	root := runs.RunsRoot(cwd)
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
