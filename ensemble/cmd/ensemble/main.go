// Command ensemble is the flag-based CLI for ensemble: a thin REST client
// over the control-plane API server.New serves (see ensemble/server), plus
// the `up` subcommand that wires the whole binary together (config ->
// recorder/proxy/latency/sessions -> orchestrator -> stubs -> server) and
// runs it until interrupted. The `tui` subcommand (and `up --tui`) render
// that same API as a terminal UI — see ensemble/tui.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/caribou-crew/ensemble/core/buildinfo"
)

// version is stamped by goreleaser via -X main.version at release time.
// Everywhere it's displayed or reported, go through buildinfo.Resolve so a
// local build (which leaves this at "dev") still reports the commit it was
// built from instead of a bare, indistinguishable "dev".
var version = "dev"

const usage = `ensemble — local backend orchestrator CLI

Usage:
  ensemble up [-c ensemble.yaml] [--profile p1,p2] [--variant svc=name,...] [--api 127.0.0.1:4700] [--tui] [profile...]
                 (with profile names: adds them to a running stack, else cold-starts with them active)
  ensemble dashboard [--api-url URL] [--no-open]
  ensemble tui [--api-url URL]
  ensemble status [--api-url URL] [--json]
  ensemble ready [--api-url URL] [--json] [--timeout DURATION]
  ensemble down [--api-url URL] [--json] [profile...]   (with profile names: deactivates just those)
  ensemble profiles [--api-url URL] [--json]
  ensemble seed <name> [--api-url URL] [--json]
  ensemble variant <service> <variant> [--api-url URL] [--json]
  ensemble restart <service> [--api-url URL] [--json]
  ensemble latency list [--api-url URL] [--json]
  ensemble latency set --target NAME --path / [--fixed MS] [--p50 MS] [--p95 MS] [--p99 MS] [--enabled] [--api-url URL] [--json]
  ensemble latency reset [--api-url URL] [--json]
  ensemble latency arm-all --enabled=true|false [--api-url URL] [--json]
  ensemble traffic [--since N] [--errors-only] [--follow] [--api-url URL] [--json]
  ensemble trace <traceId> [--export har|curl|raw] [--api-url URL] [--json]
  ensemble --version

Env:
  ENSEMBLE_API   default --api-url for client commands (default http://127.0.0.1:4700)
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is the CLI's testable entrypoint: parses the top-level command and
// dispatches, writing to stdout/stderr rather than the process' own
// directly so tests can capture output in-process instead of exec'ing a
// subprocess. Returns the process exit code.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}

	switch args[0] {
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return 0
	case "--version", "-version":
		fmt.Fprintln(stdout, buildinfo.Resolve(version))
		return 0
	case "up":
		return cmdUp(args[1:], stdout, stderr)
	case "dashboard":
		return cmdDashboard(args[1:], stdout, stderr)
	case "tui":
		return cmdTUI(args[1:], stdout, stderr)
	case "status":
		return cmdStatus(args[1:], stdout, stderr)
	case "ready":
		return cmdReady(args[1:], stdout, stderr)
	case "down":
		return cmdDown(args[1:], stdout, stderr)
	case "seed":
		return cmdSeed(args[1:], stdout, stderr)
	case "variant":
		return cmdVariant(args[1:], stdout, stderr)
	case "restart":
		return cmdRestart(args[1:], stdout, stderr)
	case "profiles":
		return cmdProfiles(args[1:], stdout, stderr)
	case "latency":
		return cmdLatency(args[1:], stdout, stderr)
	case "traffic":
		return cmdTraffic(args[1:], stdout, stderr)
	case "trace":
		return cmdTrace(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "ensemble: unknown command %q\n", args[0])
		fmt.Fprint(stderr, usage)
		return 2
	}
}

// defaultAPIURL resolves the client commands' default --api-url: the
// ENSEMBLE_API env var when set, else the `up` default listen address.
func defaultAPIURL() string {
	if v := os.Getenv("ENSEMBLE_API"); v != "" {
		return v
	}
	return "http://127.0.0.1:4700"
}
