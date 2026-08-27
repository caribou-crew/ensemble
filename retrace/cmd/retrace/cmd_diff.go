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
		roots         rootList
	)
	fs.Var(&roots, "root", "repository directory to search for runs; repeatable (default: the working directory). With more than one, a selector may name its app: web@latest")
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

	searchRoots := roots.resolve(cwd)

	a, err := resolveSide(searchRoots, appName, *flow, *aSel)
	if err != nil {
		return fail(stderr, "diff: %v", err)
	}
	// "none" means "I could not compare", NEVER "nothing differed". Exit 3
	// (could not evaluate), naming the verb that fixes it — never a diff
	// against an empty directory, which would report every call as missing.
	if a.Kind == "none" {
		return fail(stderr, "diff: no reference bundle for side a: %s\nrun `retrace ref accept --flow %s` once this flow has a good run", noneReason(searchRoots, appName, *flow), *flow)
	}
	b, err := resolveSide(searchRoots, appName, *flow, *bSel)
	if err != nil {
		return fail(stderr, "diff: %v", err)
	}
	if b.Kind == "none" {
		return fail(stderr, "diff: no reference bundle for side b: %s\nrun `retrace ref accept --flow %s` once this flow has a good run", noneReason(searchRoots, appName, *flow), *flow)
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

// resolveSide resolves one --a/--b selector to a RunRef, searching every
// --root in turn.
//
// The selector may name its own app (`web@latest`), which is what makes a
// cross-repo comparison expressible: the two sides of one diff can come from
// two repositories recording two different clients. A bare selector uses the
// app the CLI resolved.
//
// "reference" goes through refs.Resolve — the committed bundle, else the
// newest eligible run, else nothing with a reason. The returned Reference
// maps onto a RunRef by copying Kind straight through: both use
// "bundle" | "run" | "none", one vocabulary, nothing to translate.
//
// A selector matching in MORE than one root is refused rather than resolved
// to the first. First-wins would be a silent wrong answer in the one
// situation this flag creates — two checkouts of the same app, which is what
// someone comparing a branch against main has — and the diff that came out
// would be honestly labelled and completely wrong.
func resolveSide(roots []string, defaultApp, flow, selector string) (diff.RunRef, error) {
	app, sel := splitSelector(selector, defaultApp)

	type hit struct {
		root string
		ref  diff.RunRef
	}
	var hits []hit
	var noneRef diff.RunRef
	haveNone := false

	for _, root := range roots {
		runsRoot := runs.RunsRoot(root)
		if sel == "reference" {
			r := refs.Resolve(root, runsRoot, app, flow)
			ref := diff.RunRef{RunID: r.RunID, Kind: r.Kind, Dir: r.Dir, Manifest: r.Manifest}
			if r.Kind == "none" {
				// Kept, not discarded: if NO root resolves a reference, the
				// caller still needs a RunRef whose Kind is "none" so its own
				// "run `retrace ref accept`" message fires.
				//
				// Which root's "none" it is does not matter — a none carries
				// no run id, no directory and no manifest, so they are all the
				// same value. The reason a reader needs comes from noneReason,
				// which asks every root.
				noneRef, haveNone = ref, true
				continue
			}
			hits = append(hits, hit{root, ref})
			continue
		}
		id := runs.FindRun(runsRoot, app, flow, sel)
		if id == "" {
			continue
		}
		p, err := runs.PathsFor(runsRoot, app, flow, id)
		if err != nil {
			return diff.RunRef{}, err
		}
		m, err := runs.ReadManifest(p.ManifestPath)
		if err != nil {
			return diff.RunRef{}, fmt.Errorf("reading manifest for %s/%s/%s in %s: %w", app, flow, id, root, err)
		}
		hits = append(hits, hit{root, diff.RunRef{RunID: id, Kind: "run", Dir: p.RunDir, Manifest: m}})
	}

	switch {
	case len(hits) == 1:
		return hits[0].ref, nil
	case len(hits) > 1:
		var where []string
		for _, h := range hits {
			where = append(where, fmt.Sprintf("%s (%s)", h.root, h.ref.RunID))
		}
		return diff.RunRef{}, fmt.Errorf("%q matches a run in more than one root: %s — name the one you mean with a single --root, or select it by run id",
			selector, strings.Join(where, ", "))
	case haveNone:
		return noneRef, nil
	}
	return diff.RunRef{}, fmt.Errorf("no run matches %q for %s/%s in %s", selector, app, flow, strings.Join(roots, ", "))
}

// noneReason re-asks refs.Resolve for the explanation behind a "none", so
// the refusal names the runs it tried instead of only saying there is no
// reference. Resolving twice is cheap (a few Stats and one JSON decode) and
// keeps resolveSide's return type the RunRef every other caller wants.
//
// Reported per root, because with several of them "there is no reference"
// is not one fact — each tree has its own reason, and the one the reader
// needs is whichever tree they thought they had accepted a bundle in.
func noneReason(roots []string, app, flow string) string {
	var parts []string
	for _, root := range roots {
		r := refs.Resolve(root, runs.RunsRoot(root), app, flow)
		if r.Reason == "" {
			continue
		}
		if len(roots) == 1 {
			return r.Reason
		}
		parts = append(parts, fmt.Sprintf("%s: %s", root, r.Reason))
	}
	if len(parts) == 0 {
		return "no reference resolved"
	}
	return strings.Join(parts, "; ")
}
