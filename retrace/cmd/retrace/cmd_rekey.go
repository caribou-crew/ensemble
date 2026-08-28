package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/caribou-crew/ensemble/retrace/config"
	"github.com/caribou-crew/ensemble/retrace/reckey"
)

// cmdRekey rotates the team key: every encryption.json under .retrace/runs
// and .retrace-ref is rewrapped from --old to --new, so rotating never
// requires re-recording anything (design.md's goal). --init is the other,
// simpler path: writing a fresh key for a project that has none yet.
//
// Losing --old loses every field encrypted under it, forever, by design —
// there is no recovery backdoor. Treat it with the same operational care as
// any other production secret.
func cmdRekey(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("rekey", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		oldKey = fs.String("old", "", "the current team key (hex or base64) — required unless --init")
		newKey = fs.String("new", "", "the team key to rotate to (hex or base64) — required unless --init")
		init_  = fs.Bool("init", false, "write a fresh team key to .retrace/recording.key for a project that has none yet, then exit — never overwrites an existing key")
		asJSON = fs.Bool("json", false, "emit the result as JSON on stdout")
	)
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fail(stderr, "rekey: cannot determine the working directory: %v", err)
	}
	cfg, err := config.Discover(cwd)
	if err != nil {
		return fail(stderr, "%v", err)
	}

	if *init_ {
		if strings.TrimSpace(*oldKey) != "" || strings.TrimSpace(*newKey) != "" {
			return fail(stderr, "rekey: --init writes a fresh key and takes no --old/--new — that pair rotates an EXISTING key, which --init has nothing to rotate from")
		}
		path, err := reckey.InitKeyFile(cfg.Dir)
		if err != nil {
			return fail(stderr, "rekey: %v", err)
		}
		if *asJSON {
			if err := writeJSON(stdout, map[string]any{"initialized": path}); err != nil {
				return fail(stderr, "rekey: %v", err)
			}
		} else {
			fmt.Fprintf(stdout, "retrace: wrote a fresh team key to %s\n", path)
			fmt.Fprintf(stdout, "  keep it safe: losing it loses every field encrypted under it, forever\n")
			fmt.Fprintf(stdout, "  in CI, set the same bytes as the RETRACE_RECORDING_KEY secret\n")
		}
		return exitOK
	}

	if strings.TrimSpace(*oldKey) == "" || strings.TrimSpace(*newKey) == "" {
		return fail(stderr, "rekey: --old and --new are both required (or pass --init to write a first key for a project that has none)")
	}
	old, err := reckey.ParseKey(*oldKey)
	if err != nil {
		return fail(stderr, "rekey: --old: %v", err)
	}
	next, err := reckey.ParseKey(*newKey)
	if err != nil {
		return fail(stderr, "rekey: --new: %v", err)
	}

	res, err := reckey.Rekey(reckey.RekeyOptions{Cwd: cwd, Old: old, New: next})
	if err != nil {
		return fail(stderr, "rekey: %v", err)
	}

	if *asJSON {
		if err := writeJSON(stdout, res); err != nil {
			return fail(stderr, "rekey: %v", err)
		}
	} else {
		renderRekey(stdout, res)
	}

	// NeedsAttention excludes the ordinary "already on --new" no-op — only a
	// read failure or a file wrapped under some third, unrelated key means
	// this pass left something an operator has to look at.
	if len(res.NeedsAttention()) > 0 {
		return exitGate
	}
	return exitOK
}

func renderRekey(w io.Writer, res reckey.RekeyResult) {
	rewrapped := res.Rewrapped()
	skipped := res.Skipped()
	fmt.Fprintf(w, "retrace: rekey: %d rewrapped, %d skipped\n", len(rewrapped), len(skipped))
	for _, e := range rewrapped {
		fmt.Fprintf(w, "  rewrapped %s\n", e.Path)
	}
	for _, e := range skipped {
		fmt.Fprintf(w, "  %s: %s — %s\n", e.Action, e.Path, e.Reason)
	}
}
