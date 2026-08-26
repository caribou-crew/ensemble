package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/caribou-crew/ensemble/retrace/runs"
)

// runsResult is the --json document. It is an object rather than a bare
// array so a later field (a summary, a root path) can be added without
// changing the top-level type an agent's parser bound to.
type runsResult struct {
	Root string           `json:"root"`
	Runs []runs.RunStatus `json:"runs"`
	// Counts is a convenience an agent driving the loop reads instead of
	// tallying states itself — the states are a closed set, so a summary
	// cannot drift from the list the way a free-form one would.
	Counts map[runs.State]int `json:"counts"`
}

// cmdRuns lists recorded runs and what supervision makes of each.
//
// It exists because a run directory used to be unable to say whether
// anything was still writing to it. Every incident this command is for
// looked the same from outside: a directory with no manifest, which is
// either a capture that is thirty seconds from finishing or one that died
// an hour ago and left a listener behind.
//
// It is a REPORT, not a gate: it exits 0 whether or not it finds abandoned
// runs, and only returns 3 when it could not read the runs root at all.
// `retrace check` is the gating half — a listing that failed CI because
// someone Ctrl-C'd a run last week would just get piped to /dev/null.
func cmdRuns(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("runs", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		app       = fs.String("app", "", "list only this app (default: every recorded app)")
		flow      = fs.String("flow", "", "list only this flow (default: every recorded flow)")
		state     = fs.String("state", "", "list only runs in this state: running, complete or abandoned")
		asJSON    = fs.Bool("json", false, "emit the listing as JSON on stdout")
		abandonAf = fs.Duration("abandoned-after", runs.DefaultAbandonAfter,
			"how long an un-finalized run with NO recorded owner may go before it is called abandoned; runs that recorded an owner are judged by whether that process is still alive, so this bound does not apply to them")
	)
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *state != "" && !validState(*state) {
		return fail(stderr, "runs: unknown --state %q — use one of: running, complete, abandoned", *state)
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fail(stderr, "runs: cannot determine the working directory: %v", err)
	}
	root := runs.RunsRoot(cwd)
	all, err := runs.StatusAll(root, time.Now(), *abandonAf)
	if err != nil {
		// An unreadable root and an empty one must not look alike: a CI job
		// that silently listed nothing because of a permission error would
		// report "no abandoned runs" for exactly the reason it should have
		// reported a problem.
		return fail(stderr, "runs: cannot read %s: %v", root, err)
	}

	filtered := all[:0:0]
	for _, st := range all {
		if *app != "" && st.App != *app {
			continue
		}
		if *flow != "" && st.Flow != *flow {
			continue
		}
		if *state != "" && string(st.State) != *state {
			continue
		}
		filtered = append(filtered, st)
	}

	counts := map[runs.State]int{}
	for _, st := range filtered {
		counts[st.State]++
	}

	if *asJSON {
		if err := writeJSON(stdout, runsResult{Root: root, Runs: filtered, Counts: counts}); err != nil {
			return fail(stderr, "runs: %v", err)
		}
		return exitOK
	}
	printRuns(stdout, root, filtered, counts)
	return exitOK
}

func validState(s string) bool {
	switch runs.State(s) {
	case runs.StateRunning, runs.StateComplete, runs.StateAbandoned:
		return true
	}
	return false
}

// stateMark is the one-glyph column. Abandoned gets the loud one because it
// is the only state that needs a human: a complete run is finished and a
// running one will finish or become abandoned on its own.
func stateMark(s runs.State) string {
	switch s {
	case runs.StateComplete:
		return "✓"
	case runs.StateRunning:
		return "•"
	case runs.StateAbandoned:
		return "⚠"
	}
	return "?"
}

func printRuns(w io.Writer, root string, list []runs.RunStatus, counts map[runs.State]int) {
	if len(list) == 0 {
		fmt.Fprintf(w, "no runs recorded under %s\n", root)
		return
	}
	// Column widths from the data, so a long app name does not ragged the
	// whole table and a short one does not waste half the terminal.
	wApp, wFlow, wRun := len("APP"), len("FLOW"), len("RUN")
	for _, st := range list {
		wApp = max(wApp, len(st.App))
		wFlow = max(wFlow, len(st.Flow))
		wRun = max(wRun, len(st.RunID))
	}
	fmt.Fprintf(w, "  %-*s  %-*s  %-*s  %-9s  %s\n", wApp, "APP", wFlow, "FLOW", wRun, "RUN", "STATE", "AGE")
	for _, st := range list {
		fmt.Fprintf(w, "%s %-*s  %-*s  %-*s  %-9s  %s\n",
			stateMark(st.State), wApp, st.App, wFlow, st.Flow, wRun, st.RunID,
			st.State, humanAge(st.AgeSeconds))
	}

	// The reason is printed only for the state that needs acting on.
	// Printing it for every row would bury the one line that matters.
	for _, st := range list {
		if st.State == runs.StateAbandoned {
			fmt.Fprintf(w, "\n⚠ %s/%s/%s — %s\n", st.App, st.Flow, st.RunID, st.Reason)
		}
	}
	fmt.Fprintf(w, "\n%s\n", summarize(counts))
	if counts[runs.StateAbandoned] > 0 {
		fmt.Fprintf(w, "\nAn abandoned run's directory may hold a partial wire plane and its\nlisteners may still be bound. `retrace check` says which are still held.\n")
	}
}

// summarize renders the tally in a fixed state order, never map order — a
// summary line that reshuffles between invocations is one nobody can diff.
func summarize(counts map[runs.State]int) string {
	order := []runs.State{runs.StateComplete, runs.StateRunning, runs.StateAbandoned}
	parts := make([]string, 0, len(order))
	for _, s := range order {
		if counts[s] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[s], s))
		}
	}
	// Any state not in `order` would silently vanish from the tally; a new
	// State constant must show up here rather than be rounded away.
	var extra []string
	for s, n := range counts {
		if n > 0 && s != runs.StateComplete && s != runs.StateRunning && s != runs.StateAbandoned {
			extra = append(extra, fmt.Sprintf("%d %s", n, s))
		}
	}
	sort.Strings(extra)
	parts = append(parts, extra...)
	if len(parts) == 0 {
		return "0 runs"
	}
	return strings.Join(parts, ", ")
}

// humanAge keeps the age column narrow. Precision below a minute does not
// survive the time it takes to read the table.
func humanAge(sec int) string {
	if sec < 0 {
		// A run id stamped in the future (a clock that moved backwards, a
		// bundle copied from another machine). Reported, never rounded to
		// zero, because it also makes every age judgement below suspect.
		return fmt.Sprintf("in %s", time.Duration(-sec)*time.Second)
	}
	switch d := time.Duration(sec) * time.Second; {
	case d < time.Minute:
		return fmt.Sprintf("%ds", sec)
	case d < time.Hour:
		return fmt.Sprintf("%dm", sec/60)
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%dm", sec/3600, (sec%3600)/60)
	default:
		return fmt.Sprintf("%dd%dh", sec/86400, (sec%86400)/3600)
	}
}
