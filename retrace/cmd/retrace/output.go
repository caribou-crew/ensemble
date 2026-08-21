package main

import (
	"encoding/json"
	"fmt"
	"io"
)

// Exit codes are the CI contract. Every command returns one of these and
// nothing else, so a pipeline can branch on "needs review" vs "is broken"
// without parsing output.
const (
	exitOK   = 0 // no differences, nothing to review
	exitDiff = 1 // differences found — review required
	exitGate = 2 // a hard gate failed (rule violation, hopRequire, >=400, perf, capture)
	// exitUsage is "could not evaluate", not merely "you typed it wrong":
	// bad flags, an unreadable config, an I/O failure — and, from `retrace
	// diff`, a quarantined comparison, which is now the primary meaning of
	// 3 in that command (diff.ExitCode returns it for VerdictQuarantined).
	// --no-fail deliberately does not zero any of them.
	exitUsage = 3
)

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func fail(stderr io.Writer, format string, args ...any) int {
	fmt.Fprintf(stderr, "retrace: "+format+"\n", args...)
	return exitUsage
}
