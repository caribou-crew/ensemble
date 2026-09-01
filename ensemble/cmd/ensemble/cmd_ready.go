package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/caribou-crew/ensemble/ensemble/config"
	"github.com/caribou-crew/ensemble/ensemble/orchestrator"
)

// crashedServices names every service currently in StatusCrashed, sorted
// by appearance — the fail-fast trigger for `ensemble ready`.
func crashedServices(services []orchestrator.ServiceState) []string {
	var out []string
	for _, s := range services {
		if s.Status == orchestrator.StatusCrashed {
			out = append(out, s.Name)
		}
	}
	return out
}

// readinessPollInterval is how often `ensemble ready` re-polls GET
// /api/status while waiting for the orchestrator's readiness state to
// settle — the orchestrator does its own server-side retrying (see
// orchestrator.Orchestrator.beginReadiness); this is just how often the
// CLI checks in on that server-side state.
const readinessPollInterval = 500 * time.Millisecond

// cmdReady blocks until the orchestrator's readiness state resolves to
// ready or not_ready, or --timeout elapses (default: the same
// config.DefaultReadinessTimeoutS the orchestrator itself falls back to
// when readiness.timeout_s is unset) — exiting 0 if ready, non-zero
// otherwise. A stack with no readiness: configured settles to ready as
// soon as on_ready completes, so this returns immediately in that case.
func cmdReady(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ready", flag.ContinueOnError)
	fs.SetOutput(stderr)
	apiURL := fs.String("api-url", defaultAPIURL(), "ensemble control-plane API base URL")
	jsonOut := fs.Bool("json", false, "output JSON")
	timeout := fs.Duration("timeout", time.Duration(config.DefaultReadinessTimeoutS)*time.Second, "how long to wait for the stack to become ready before giving up")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	c := NewClient(*apiURL)
	deadline := time.Now().Add(*timeout)

	var res StatusResponse
	for {
		var err error
		res, err = c.Status(context.Background())
		if err != nil {
			fmt.Fprintf(stderr, "ensemble: ready: %v\n", err)
			return 1
		}
		// A crashed service fails fast: a crash never heals on its own (no
		// auto-restart), so waiting out the timeout would only delay the
		// same non-zero exit.
		if crashed := crashedServices(res.Services); len(crashed) > 0 {
			if *jsonOut {
				if code := printJSON(stdout, map[string]any{"ready": false, "crashed": crashed, "checks": res.Readiness.Checks}); code != 0 {
					return code
				}
				return 1
			}
			fmt.Fprintf(stderr, "ensemble: not ready: crashed: %s\n", strings.Join(crashed, ", "))
			return 1
		}
		if res.Readiness.State == orchestrator.ReadinessReady || res.Readiness.State == orchestrator.ReadinessNotReady {
			break
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(readinessPollInterval)
	}

	ready := res.Readiness.State == orchestrator.ReadinessReady
	if *jsonOut {
		if code := printJSON(stdout, map[string]any{"ready": ready, "checks": res.Readiness.Checks}); code != 0 {
			return code
		}
		if ready {
			return 0
		}
		return 1
	}

	if ready {
		fmt.Fprintln(stdout, "ensemble: ready")
		return 0
	}

	fmt.Fprintln(stderr, "ensemble: not ready:")
	for _, chk := range res.Readiness.Checks {
		if !chk.Passed {
			fmt.Fprintf(stderr, "  - %s: %s\n", chk.Name, chk.LastError)
		}
	}
	return 1
}
