package main

import (
	"context"
	"flag"
	"fmt"
	"io"
)

// cmdRestart stops and restarts one named service in place — same
// placement/variant it was already running — via
// POST /api/services/{name}/restart. The CLI counterpart to the dashboard's
// existing per-service restart action, for a one-off fix without a full
// config-reconcile (`ensemble up`) or down/up cycle.
func cmdRestart(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("restart", flag.ContinueOnError)
	fs.SetOutput(stderr)
	apiURL := fs.String("api-url", defaultAPIURL(), "ensemble control-plane API base URL")
	jsonOut := fs.Bool("json", false, "output JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: ensemble restart <service> [--api-url URL] [--json]")
		return 2
	}
	name := fs.Arg(0)

	c := NewClient(*apiURL)
	st, err := c.Restart(context.Background(), name)
	if err != nil {
		fmt.Fprintf(stderr, "ensemble: restart: %v\n", err)
		return 1
	}
	if *jsonOut {
		return printJSON(stdout, st)
	}
	fmt.Fprintf(stdout, "%s: restarted (%s, pid %d)\n", st.Name, st.Status, st.PID)
	return 0
}
