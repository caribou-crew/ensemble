package main

import (
	"context"
	"flag"
	"fmt"
	"io"
)

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
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%d\t%d\t%s\n", s.Name, s.Status, s.Placement, s.Variant, s.PID, s.Port, s.ProxyPort, s.LastErr)
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
	return 0
}
