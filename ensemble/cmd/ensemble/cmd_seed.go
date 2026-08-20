package main

import (
	"context"
	"flag"
	"fmt"
	"io"
)

func cmdSeed(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("seed", flag.ContinueOnError)
	fs.SetOutput(stderr)
	apiURL := fs.String("api-url", defaultAPIURL(), "ensemble control-plane API base URL")
	jsonOut := fs.Bool("json", false, "output JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: ensemble seed <name>")
		return 2
	}
	name := fs.Arg(0)

	c := NewClient(*apiURL)
	res, err := c.Seed(context.Background(), name)
	if err != nil {
		fmt.Fprintf(stderr, "ensemble: seed: %v\n", err)
		return 1
	}

	if *jsonOut {
		if code := printJSON(stdout, res); code != 0 {
			return code
		}
	} else {
		tw := newTabwriter(stdout)
		fmt.Fprintln(tw, "KIND\tREF\tOK\tDURATION(ms)\tERROR")
		for _, r := range res.Results {
			fmt.Fprintf(tw, "%s\t%s\t%t\t%.1f\t%s\n", r.Kind, r.Ref, r.OK, r.DurationMs, r.Err)
		}
		tw.Flush()
	}

	if !res.OK {
		fmt.Fprintf(stderr, "ensemble: seed %q failed: %s\n", name, res.Error)
		return 1
	}
	return 0
}
