package main

import (
	"context"
	"flag"
	"fmt"
	"io"
)

// cmdVariant switches a service to one of its config-declared variants
// (`variants:` in ensemble.yaml) via POST /api/services/{name}/variant.
func cmdVariant(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("variant", flag.ContinueOnError)
	fs.SetOutput(stderr)
	apiURL := fs.String("api-url", defaultAPIURL(), "ensemble control-plane API base URL")
	jsonOut := fs.Bool("json", false, "output JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 2 {
		fmt.Fprintln(stderr, "usage: ensemble variant <service> <variant> [--api-url URL] [--json]")
		return 2
	}
	name, variant := fs.Arg(0), fs.Arg(1)

	c := NewClient(*apiURL)
	st, err := c.SetVariant(context.Background(), name, variant)
	if err != nil {
		fmt.Fprintf(stderr, "ensemble: variant: %v\n", err)
		return 1
	}
	if *jsonOut {
		return printJSON(stdout, st)
	}
	fmt.Fprintf(stdout, "%s: variant %s (%s, pid %d)\n", st.Name, st.Variant, st.Status, st.PID)
	return 0
}
