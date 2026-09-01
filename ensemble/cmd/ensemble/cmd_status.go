package main

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/caribou-crew/ensemble/ensemble/config"
	"github.com/caribou-crew/ensemble/ensemble/orchestrator"
)

// statusLabel renders a service's status cell, appending how the process
// ended for the exited/crashed states — "crashed (exit 1)",
// "crashed (signal killed)" — so the exit detail is visible without --json.
func statusLabel(s orchestrator.ServiceState) string {
	switch {
	case s.ExitCode != nil:
		return fmt.Sprintf("%s (exit %d)", s.Status, *s.ExitCode)
	case s.Signal != "":
		return fmt.Sprintf("%s (signal %s)", s.Status, s.Signal)
	default:
		return string(s.Status)
	}
}

func cmdStatus(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	apiURL := fs.String("api-url", defaultAPIURL(), "ensemble control-plane API base URL")
	jsonOut := fs.Bool("json", false, "output JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	c := NewClient(*apiURL)
	res, err := c.Status(context.Background())
	if err != nil {
		fmt.Fprintf(stderr, "ensemble: status: %v\n", err)
		return 1
	}
	if *jsonOut {
		return printJSON(stdout, res)
	}

	tw := newTabwriter(stdout)
	fmt.Fprintln(tw, "NAME\tSTATUS\tPLACEMENT\tVARIANT\tPID\tPORT\tPROXY\tERROR")
	for _, s := range res.Services {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%d\t%d\t%s\n", s.Name, statusLabel(s), s.Placement, s.Variant, s.PID, s.Port, s.ProxyPort, s.LastErr)
	}
	tw.Flush()

	if len(res.Readiness.Checks) > 0 {
		passed := 0
		for _, c := range res.Readiness.Checks {
			if c.Passed {
				passed++
			}
		}
		fmt.Fprintf(stdout, "\nREADINESS: %d/%d passed (%s)\n", passed, len(res.Readiness.Checks), res.Readiness.State)
	}
	printWiringWarnings(stdout, res.Warnings)
	return 0
}

// printWiringWarnings renders proxy-wiring warnings (see
// config.Config.WiringWarnings) the same way `ensemble up` does — shared so
// `status` and `up` never drift in wording. A no-op when there are none.
func printWiringWarnings(w io.Writer, warnings []config.WiringWarning) {
	if len(warnings) == 0 {
		return
	}
	fmt.Fprintln(w, "\nWIRING WARNINGS:")
	for _, wn := range warnings {
		fmt.Fprintf(w, "  %s\n", wn.Message)
	}
}
