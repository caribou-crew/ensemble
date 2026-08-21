package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/caribou-crew/ensemble/ensemble/tui"
)

// cmdTUI takes over the terminal with a live view of a running control
// plane (services, traffic, latency, profiles) — the terminal analog of
// cmdDashboard, reading the same /api surface instead of opening a browser
// tab at it. Requires `ensemble up` to already be running and reachable,
// same reachability check as cmdDashboard.
func cmdTUI(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("tui", flag.ContinueOnError)
	fs.SetOutput(stderr)
	apiURL := fs.String("api-url", defaultAPIURL(), "ensemble control-plane API base URL")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := tui.Run(ctx, *apiURL); err != nil {
		fmt.Fprintf(stderr, "ensemble: tui: %v\n", err)
		return 1
	}
	return 0
}
