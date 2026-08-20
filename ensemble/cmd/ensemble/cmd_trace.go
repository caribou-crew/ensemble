package main

import (
	"context"
	"flag"
	"fmt"
	"io"
)

func cmdTrace(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("trace", flag.ContinueOnError)
	fs.SetOutput(stderr)
	apiURL := fs.String("api-url", defaultAPIURL(), "ensemble control-plane API base URL")
	jsonOut := fs.Bool("json", false, "output JSON")
	export := fs.String("export", "", "export format: har|curl|raw")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: ensemble trace <traceId> [--export har|curl|raw]")
		return 2
	}
	traceID := fs.Arg(0)
	c := NewClient(*apiURL)
	ctx := context.Background()

	if *export != "" {
		body, err := c.TraceExport(ctx, traceID, *export)
		if err != nil {
			fmt.Fprintf(stderr, "ensemble: trace: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, body)
		return 0
	}

	res, err := c.Trace(ctx, traceID)
	if err != nil {
		fmt.Fprintf(stderr, "ensemble: trace: %v\n", err)
		return 1
	}
	if *jsonOut {
		return printJSON(stdout, res)
	}
	printHopTable(stdout, res.Hops)
	return 0
}
