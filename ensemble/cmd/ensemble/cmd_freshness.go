package main

import (
	"context"
	"flag"
	"fmt"
	"io"
)

// cmdFreshness prints the current freshness state — behind-own-branch and
// behind-default-branch counts — for every service the orchestrator's
// background poll loop has checked. It reads the orchestrator's current
// state via GET /api/status (the same field `ensemble status --json`
// already carries) rather than triggering a new fetch: forcing a fetch on
// every invocation would make `ensemble freshness` itself the slow-network
// call it exists to spare a developer from running by hand.
func cmdFreshness(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("freshness", flag.ContinueOnError)
	fs.SetOutput(stderr)
	apiURL := fs.String("api-url", defaultAPIURL(), "ensemble control-plane API base URL")
	jsonOut := fs.Bool("json", false, "output JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	c := NewClient(*apiURL)
	res, err := c.Status(context.Background())
	if err != nil {
		fmt.Fprintf(stderr, "ensemble: freshness: %v\n", err)
		return 1
	}

	if *jsonOut {
		return printJSON(stdout, res.Services)
	}

	tw := newTabwriter(stdout)
	fmt.Fprintln(tw, "SERVICE\tBRANCH\tBEHIND\tMAIN BEHIND\tCHECKED\tERROR")
	for _, s := range res.Services {
		if s.Freshness == nil {
			continue
		}
		checked := s.Freshness.CheckedAt
		if checked == "" {
			checked = "never"
		}
		fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%s\t%s\n",
			s.Name, s.Freshness.Branch, s.Freshness.BehindBranch, s.Freshness.BehindDefault,
			checked, s.Freshness.Error)
	}
	tw.Flush()
	return 0
}
