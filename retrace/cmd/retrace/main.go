// Command retrace records a flow through a local stack, replays it as strict
// mocks in CI, diffs two runs on pixels/wire/hops, and serves a review
// queue. It is a thin dispatcher: every subcommand lives in its own
// cmd_*.go and returns a process exit code (see output.go).
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

const usage = `retrace — record / replay / diff / review flows

Usage:
  retrace run --flow NAME [--app NAME] [--ensemble URL] [--no-ensemble] [--upstream URL] [--json] [--no-config] -- <test command>
  retrace diff --flow NAME [--app NAME] [--a SELECTOR] [--b SELECTOR] [--json] [--images=false] [--out DIR] [--allow-degraded] [--no-fail]
  retrace replay --ref FLOW [--app NAME] [--listen 127.0.0.1:0] [--json] -- <test command>
  retrace revalidate --ref FLOW [--app NAME] --upstream URL [--json]
  retrace ref list|accept|reject [--flow NAME] [--app NAME] [--run SELECTOR] [--json]
  retrace serve [--addr 127.0.0.1:4800] [--allow-host HOST] [--open]
  retrace export --out DIR [--flow NAME] [--app NAME] [--json]
  retrace --version

Exit codes:
  0 no differences        1 differences to review   2 hard gate failed
  3 could not evaluate: a quarantined side, bad flags, unreadable config, or I/O failure

  --no-fail forces 0 for a "changed" or "failed" verdict. It does NOT zero a
  quarantine: 3 means nothing was compared, which is not a finding to suppress.

Env:
  RETRACE_RUN_DIR     set by ` + "`retrace run`" + ` for adapters (checkpoints, markers)
  RETRACE_PROXY_URL   set by ` + "`retrace run`" + `; point the app under test at it
  RETRACE_MARKER_URL  set by ` + "`retrace run`" + `; HTTP-only runners post markers here
  RETRACE_UPSTREAM_URL set by ` + "`retrace run`" + ` when --upstream/config names one; sign
                      URL-bound auth (DPoP/RFC 9449 etc.) against this, transport via
                      RETRACE_PROXY_URL — absent when the run has no upstream configured
  RETRACE_STRICT      1/true/yes/on = adapters fail loudly when the handshake env is
                      absent (0/false/no/off/unset = quiet no-op; any other value is
                      a startup error, naming the value and the accepted set)
  ENSEMBLE_API        default for --ensemble (http://127.0.0.1:4700)
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
		fmt.Fprintln(stdout, buildinfo.Resolve(version))
		return exitOK
	case "run":
		return cmdRun(args[1:], stdout, stderr)
	case "diff":
		return cmdDiff(args[1:], stdout, stderr)
	case "replay":
		return cmdReplay(args[1:], stdout, stderr)
	case "revalidate":
		return cmdRevalidate(args[1:], stdout, stderr)
	case "ref":
		return cmdRef(args[1:], stdout, stderr)
	case "serve":
		return cmdServe(args[1:], stdout, stderr)
	case "export":
		return cmdExport(args[1:], stdout, stderr)
	default:
		return fail(stderr, "unknown command %q\n\n%s", args[0], usage)
	}
}
