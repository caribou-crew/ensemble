package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
)

func cmdTraffic(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("traffic", flag.ContinueOnError)
	fs.SetOutput(stderr)
	apiURL := fs.String("api-url", defaultAPIURL(), "ensemble control-plane API base URL")
	jsonOut := fs.Bool("json", false, "output JSON")
	since := fs.Uint64("since", 0, "only hops with seq > since")
	errorsOnly := fs.Bool("errors-only", false, "only hops with status>=400 or a transport error")
	follow := fs.Bool("follow", false, "stream live hops via SSE (blocks until interrupted)")
	session := fs.String("session", "", "only hops carrying this session id; with --export, export the whole session instead of listing it")
	export := fs.String("export", "", "with --session, export format: har")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	c := NewClient(*apiURL)

	if *export != "" {
		if *session == "" {
			fmt.Fprintln(stderr, "ensemble: traffic: --export requires --session <id>")
			return 2
		}
		body, err := c.SessionExport(context.Background(), *session, *export)
		if err != nil {
			fmt.Fprintf(stderr, "ensemble: traffic: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, body)
		return 0
	}

	if *follow {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return followTraffic(ctx, c, *since, *jsonOut, stdout, stderr)
	}

	res, err := c.TrafficFiltered(context.Background(), *since, 0, *errorsOnly, *session)
	if err != nil {
		fmt.Fprintf(stderr, "ensemble: traffic: %v\n", err)
		return 1
	}
	if *jsonOut {
		return printJSON(stdout, res)
	}
	printHopTable(stdout, res.Hops)
	return 0
}

// followTraffic streams hops via SSE until ctx is canceled or the stream
// closes, printing each as it arrives (one JSON object per line with
// --json, one compact table-less line otherwise).
func followTraffic(ctx context.Context, c *Client, since uint64, jsonOut bool, stdout, stderr io.Writer) int {
	ch, err := c.TrafficStream(ctx, since)
	if err != nil {
		fmt.Fprintf(stderr, "ensemble: traffic: %v\n", err)
		return 1
	}
	enc := json.NewEncoder(stdout)
	for {
		select {
		case <-ctx.Done():
			return 0
		case h, ok := <-ch:
			if !ok {
				return 0
			}
			if jsonOut {
				_ = enc.Encode(h)
			} else {
				printHopLine(stdout, h)
			}
		}
	}
}
