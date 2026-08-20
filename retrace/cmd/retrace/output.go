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
	exitOK    = 0 // no differences, nothing to review
	exitDiff  = 1 // differences found — review required
	exitGate  = 2 // a hard gate failed (rule violation, hopRequire, >=400, perf, capture)
	exitUsage = 3 // bad flags, unreadable config, I/O failure
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
