package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/caribou-crew/ensemble/ensemble/orchestrator"
)

// cmdProfiles lists every configured profile with its members and whether
// it's active on the running stack (GET /api/profiles).
func cmdProfiles(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("profiles", flag.ContinueOnError)
	fs.SetOutput(stderr)
	apiURL := fs.String("api-url", defaultAPIURL(), "ensemble control-plane API base URL")
	jsonOut := fs.Bool("json", false, "output JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	c := NewClient(*apiURL)
	st, err := c.Profiles(context.Background())
	if err != nil {
		fmt.Fprintf(stderr, "ensemble: profiles: %v\n", err)
		return 1
	}
	if *jsonOut {
		return printJSON(stdout, st)
	}
	printProfiles(stdout, st)
	return 0
}

func printProfiles(w io.Writer, st orchestrator.ProfilesState) {
	tw := newTabwriter(w)
	fmt.Fprintln(tw, "PROFILE\tACTIVE\tSERVICES")
	for _, p := range st.Profiles {
		active := "-"
		if p.Active {
			active = "yes"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", p.Name, active, strings.Join(p.Services, ", "))
	}
	tw.Flush()
}

// switchProfiles drives POST /api/profiles/{name}/up|down for each name
// against a running stack, printing the resulting profile table.
func switchProfiles(c *Client, names []string, up bool, jsonOut bool, stdout, stderr io.Writer) int {
	verb := "down"
	if up {
		verb = "up"
	}
	var st orchestrator.ProfilesState
	for _, name := range names {
		var err error
		if up {
			st, err = c.ProfileUp(context.Background(), name)
		} else {
			st, err = c.ProfileDown(context.Background(), name)
		}
		if err != nil {
			fmt.Fprintf(stderr, "ensemble: %s %s: %v\n", verb, name, err)
			return 1
		}
	}
	if jsonOut {
		return printJSON(stdout, st)
	}
	fmt.Fprintf(stdout, "ensemble: profile %s: %s (attached to %s)\n", verb, strings.Join(names, ", "), c.BaseURL)
	printProfiles(stdout, st)
	return 0
}
