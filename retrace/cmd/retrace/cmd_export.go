package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/caribou-crew/ensemble/core/buildinfo"
	"github.com/caribou-crew/ensemble/retrace/config"
	"github.com/caribou-crew/ensemble/retrace/serve"
)

// cmdExport writes the review queue as a static CI artifact: a directory
// that opens with file://, needs no server, and is read by someone who was
// not there when it was produced.
//
// Its exit code is the worst diff.ExitCode across everything it exported, so
// `retrace export` can be the only step in a CI job. That contract has FOUR
// values, not three: 0 pass, 1 changed, 2 failed and **3 quarantined** — the
// highest, because "nobody could evaluate this" must not exit like something
// that was evaluated and found merely different. The mapping is
// diff.ExitCode's and is not re-derived here or in serve; see
// serve.exitCodeFor.
func cmdExport(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		out    = fs.String("out", "", "directory to write the static report into (required)")
		app    = fs.String("app", "", "export only this app (default: every recorded app)")
		flow   = fs.String("flow", "", "export only this flow (default: every recorded flow)")
		asJSON = fs.Bool("json", false, "emit the ExportResult as JSON on stdout")
	)
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	// --out has no default ON PURPOSE. A CI artifact written into whatever
	// directory the job happened to be standing in is a report nobody
	// uploads, and "the report was empty" and "the report was written
	// somewhere else" look identical afterwards.
	if strings.TrimSpace(*out) == "" {
		return fail(stderr, "export: --out DIR is required — it names the directory the report is written to, and there is no sensible default for an artifact a CI job has to upload")
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fail(stderr, "export: cannot determine the working directory: %v", err)
	}
	cfg, err := config.Discover(cwd)
	if err != nil {
		return fail(stderr, "%v", err)
	}

	res, err := serve.Export(serve.ExportOptions{
		Deps:   serve.Deps{Cwd: cwd, Cfg: cfg, Version: buildinfo.Resolve(version)},
		OutDir: *out, App: *app, Flow: *flow,
	})
	if err != nil {
		return fail(stderr, "export: %v", err)
	}

	// On STDERR, and in both output modes: an export of a project that has
	// recorded nothing produces a valid, well-formed, completely empty
	// report, and the exit code below it is 0. The report itself says so on
	// its front page (serve.EmptyReasonFor), but a CI log is the other place
	// somebody looks, and a silent 0 there reads as a pass.
	if res.Items == 0 {
		fmt.Fprintf(stderr, "retrace: warning: %s contains no flows — nothing has been recorded under this project, so this report says nothing about whether it is healthy. The exit code below reflects the flows that were exported, and there were none.\n", res.Dir)
	}

	if *asJSON {
		if err := writeJSON(stdout, res); err != nil {
			return fail(stderr, "export: %v", err)
		}
	} else {
		fmt.Fprintf(stdout, "retrace export: %d flow(s), %d file(s) → %s\n", res.Items, len(res.Files), res.Dir)
		fmt.Fprintf(stdout, "open %s to read it; no server is needed.\n", "file://"+res.Dir+"/index.html")
	}
	return res.ExitCode
}
