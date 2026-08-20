package main

import (
	"context"
	"flag"
	"fmt"
	"io"
)

// cmdDown sends the SIGINT-equivalent POST /api/shutdown to a running
// `ensemble up` process (see server.Deps.Shutdown / handleShutdown).
func cmdDown(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("down", flag.ContinueOnError)
	fs.SetOutput(stderr)
	apiURL := fs.String("api-url", defaultAPIURL(), "ensemble control-plane API base URL")
	jsonOut := fs.Bool("json", false, "output JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	c := NewClient(*apiURL)
	res, err := c.Shutdown(context.Background())
	if err != nil {
		fmt.Fprintf(stderr, "ensemble: down: %v\n", err)
		return 1
	}
	if *jsonOut {
		return printJSON(stdout, res)
	}
	fmt.Fprintln(stdout, "ensemble: shutdown requested")
	return 0
}
