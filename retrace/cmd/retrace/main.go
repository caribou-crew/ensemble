// Command retrace records a flow through a local stack, replays it as strict
// mocks in CI, diffs two runs on pixels/wire/hops, and serves a review
// queue. It is a thin dispatcher: every subcommand lives in its own
// cmd_*.go and returns a process exit code (see output.go).
package main

import (
	"fmt"
	"io"
	"os"
)

// version is stamped by goreleaser via -X main.version at release time.
var version = "dev"

const usage = `retrace — record / replay / diff / review flows

Usage:
  retrace run --flow NAME [--app NAME] [--ensemble URL] [--upstream URL] -- <test command>
  retrace diff --flow NAME [--app NAME] [--a SELECTOR] [--b SELECTOR] [--json]
  retrace replay --ref FLOW [--app NAME] [--listen 127.0.0.1:0] -- <test command>
  retrace revalidate --ref FLOW [--app NAME] --upstream URL [--json]
  retrace ref accept|reject|list --flow NAME [--app NAME] [--run SELECTOR]
  retrace serve [--addr 127.0.0.1:4800] [--open]
  retrace export --out DIR [--flow NAME] [--app NAME]
  retrace --version

Exit codes:
  0 no differences   1 differences to review   2 hard gate failed   3 usage/IO error

Env:
  RETRACE_RUN_DIR     set by ` + "`retrace run`" + ` for adapters (checkpoints, markers)
  RETRACE_PROXY_URL   set by ` + "`retrace run`" + `; point the app under test at it
  RETRACE_MARKER_URL  set by ` + "`retrace run`" + `; HTTP-only runners post markers here
  RETRACE_STRICT      1 = adapters fail loudly when the handshake env is absent
`

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

// run is the testable entrypoint: it writes to the supplied writers rather
// than the process' own, so CLI tests capture output in-process instead of
// exec'ing a subprocess.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return exitOK
	case "--version", "-version":
		fmt.Fprintln(stdout, version)
		return exitOK
	default:
		return fail(stderr, "unknown command %q\n\n%s", args[0], usage)
	}
}
