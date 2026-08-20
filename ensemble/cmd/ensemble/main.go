// Command ensemble is the flag-based CLI for ensemble: a thin REST client
// over the control-plane API server.New serves (see ensemble/server), plus
// the `up` subcommand that wires the whole binary together (config ->
// recorder/proxy/latency/sessions -> orchestrator -> stubs -> server) and
// runs it until interrupted.
//
// Scope ruling (task 2.6 brief): the Ink-style TUI cockpit is Phase
// 3-adjacent and deferred — this ships the flag-based CLI only, no
// bubbletea dependency.
package main

import (
	"fmt"
	"io"
	"os"
)

// version is stamped by goreleaser via -X main.version at release time.
var version = "dev"

const usage = `ensemble — local backend orchestrator CLI

Usage:
  ensemble up [-c ensemble.yaml] [--profile p1,p2] [--api 127.0.0.1:4700]
  ensemble status [--api-url URL] [--json]
  ensemble down [--api-url URL] [--json]
  ensemble seed <name> [--api-url URL] [--json]
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
		fmt.Fprintln(stdout, version)
		return 0
	case "up":
		return cmdUp(args[1:], stdout, stderr)
	case "status":
		return cmdStatus(args[1:], stdout, stderr)
	case "down":
		return cmdDown(args[1:], stdout, stderr)
	case "seed":
		return cmdSeed(args[1:], stdout, stderr)
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
