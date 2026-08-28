package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/caribou-crew/ensemble/retrace/config"
	"github.com/caribou-crew/ensemble/retrace/refs"
	"github.com/caribou-crew/ensemble/retrace/replay"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

// cmdRevalidate re-issues every recorded request against a live stack and
// reports where the live answers have moved away from the recording. It is
// the other half of replay: replay asks "did the client change?", this
// asks "did the recording go stale?" — and a team that only has the first
// question ends up disbelieving it.
func cmdRevalidate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("revalidate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		ref      = fs.String("ref", "", "flow whose reference bundle is checked (required)")
		app      = fs.String("app", "", "app name (default: config app, else the directory name)")
		upstream = fs.String("upstream", "", "base URL of the live stack (default: config upstream)")
		asJSON   = fs.Bool("json", false, "emit the report as JSON on stdout")
	)
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	flow := strings.TrimSpace(*ref)
	if flow == "" {
		return fail(stderr, "revalidate: --ref is required — name the flow to check")
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fail(stderr, "revalidate: cannot determine the working directory: %v", err)
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
	up := strings.TrimSpace(*upstream)
	if up == "" {
		up = cfg.Upstream
	}
	if up == "" {
		return fail(stderr, "revalidate: --upstream is required — name the live stack to check the recording against")
	}

	// Same rule as diff and replay: "none" is could-not-evaluate, exit 3.
	r := refs.Resolve(cwd, runs.RunsRoot(cwd), appName, flow)
	if r.Kind == "none" {
		fmt.Fprintf(stderr, "retrace: revalidate: no reference bundle for %s/%s: %s\n", appName, flow, r.Reason)
		printCandidates(stderr, r)
		fmt.Fprintf(stderr, "  run `retrace ref accept --flow %s` once this flow has a good run\n", flow)
		return exitUsage
	}

	bundle, err := replay.LoadBundle(r.Dir, cfg.Dir)
	if err != nil {
		return fail(stderr, "revalidate: %v", err)
	}
	opts, err := replayOptions(cfg)
	if err != nil {
		return fail(stderr, "revalidate: %v", err)
	}

	rep, err := replay.Revalidate(context.Background(), bundle, up, opts)
	if err != nil {
		// Could not evaluate — exit 3, never a clean report.
		return fail(stderr, "revalidate: %v", err)
	}
	// Flow comes from the manifest inside the bundle; a bundle whose
	// manifest names a different flow than the one asked for is worth
	// seeing, so the selector wins for reporting only if the manifest is
	// silent.
	if rep.Flow == "" {
		rep.Flow = flow
	}

	if *asJSON {
		if err := writeJSON(stdout, rep); err != nil {
			return fail(stderr, "revalidate: %v", err)
		}
	} else {
		renderRevalidate(stdout, appName, up, rep)
	}
	return replay.ExitCode(rep)
}

func renderRevalidate(w io.Writer, app, upstream string, rep replay.RevalReport) {
	fmt.Fprintf(w, "retrace: revalidated %s/%s against %s — %d recorded call(s) re-issued\n", app, rep.Flow, upstream, rep.Checked)
	if len(rep.Drifts) == 0 {
		fmt.Fprintf(w, "  no drift: the recording still describes what the live stack does\n")
		return
	}
	for _, d := range rep.Drifts {
		fmt.Fprintf(w, "\n  %s %s\n", d.Method, d.Path)
		if d.Status != nil {
			fmt.Fprintf(w, "    status: recorded %d, live %d\n", d.Status.Recorded, d.Status.Live)
			if d.Status.Live >= 400 && d.Status.Recorded < 400 {
				fmt.Fprintf(w, "      the live stack refused this call. If the recorded request carried a\n")
				fmt.Fprintf(w, "      credential, note that redacted headers are NOT re-sent — see `redact` in retrace.yaml.\n")
			}
		}
		for _, f := range d.Fields {
			fmt.Fprintf(w, "    %s.%s: recorded %v, live %v\n", f.Scope, f.Path, f.A, f.B)
		}
	}
	fmt.Fprintf(w, "\n  verdict: %s — re-record with `retrace run` and promote with `retrace ref accept` if the\n", rep.Verdict)
	fmt.Fprintf(w, "  live behaviour is the intended one.\n")
}
