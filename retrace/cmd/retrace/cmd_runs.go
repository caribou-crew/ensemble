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
	// Root is the FIRST root searched, kept so a consumer written against
	// the single-root shape still reads something true. Roots is the whole
	// list; a listing over several trees has no single root, and inventing
	// one would be the more convenient lie.
	//
	// All three of root, roots and a row's root are REPOSITORY directories —
	// the value you hand back to --root — and never the .retrace/runs path
	// inside one. Two fields a letter apart meaning two different paths is
	// the kind of asymmetry a consumer discovers by shipping a bug.
	Root  string   `json:"root"`
	Roots []string `json:"roots"`
	Runs  []runRow `json:"runs"`
	// Counts is a convenience an agent driving the loop reads instead of
	// tallying states itself — the states are a closed set, so a summary
	// cannot drift from the list the way a free-form one would.
	Counts map[runs.State]int `json:"counts"`
}

// runRow is one listed run plus the root it was found in. Embedded, so the
// JSON gains a "root" key beside the existing RunStatus fields rather than
// nesting them — a consumer reading .app and .state keeps working, and one
// that cares which tree a run came from now has an answer.
//
// The root is on the ROW, not only on the document, because with several
// trees "which app/flow/run" no longer identifies a run: two checkouts of
// the same repository produce two runs with the same app, the same flow, and
// occasionally the same id.
type runRow struct {
	runs.RunStatus
	Root string `json:"root"`
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
		roots rootList
	)
	fs.Var(&roots, "root", "repository directory to list runs from; repeatable (default: the working directory)")
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
	searchRoots := roots.resolve(cwd)
	now := time.Now()

	var filtered []runRow
	for _, root := range searchRoots {
		runsRoot := runs.RunsRoot(root)
		all, err := runs.StatusAll(runsRoot, now, *abandonAf)
		if err != nil {
			// An unreadable root and an empty one must not look alike: a CI
			// job that silently listed nothing because of a permission error
			// would report "no abandoned runs" for exactly the reason it
			// should have reported a problem.
			//
			// One unreadable root fails the whole listing rather than being
			// skipped with a warning. A partial listing that still exits 0 is
			// the same silent-success trap one level up: the caller asked
			// about N trees and would be answered about fewer, with nothing
			// in the exit code to say so.
			return fail(stderr, "runs: cannot read %s: %v", runsRoot, err)
		}
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
			filtered = append(filtered, runRow{RunStatus: st, Root: root})
		}
	}
	// StatusAll sorts within one root; across roots the concatenation is only
	// grouped. Sorted here so the listing is the same on every invocation and
	// two roots' runs interleave by identity rather than by argument order.
	sort.Slice(filtered, func(i, j int) bool {
		a, b := filtered[i], filtered[j]
		if a.App != b.App {
			return a.App < b.App
		}
		if a.Flow != b.Flow {
			return a.Flow < b.Flow
		}
		if a.RunID != b.RunID {
			return a.RunID < b.RunID
		}
		return a.Root < b.Root
	})

	counts := map[runs.State]int{}
	for _, st := range filtered {
		counts[st.State]++
	}

	if *asJSON {
		res := runsResult{Root: searchRoots[0], Roots: searchRoots, Runs: filtered, Counts: counts}
		if res.Runs == nil {
			res.Runs = []runRow{}
		}
		if err := writeJSON(stdout, res); err != nil {
			return fail(stderr, "runs: %v", err)
		}
		return exitOK
	}
	printRuns(stdout, searchRoots, filtered, counts)
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

func printRuns(w io.Writer, roots []string, list []runRow, counts map[runs.State]int) {
	if len(list) == 0 {
		fmt.Fprintf(w, "no runs recorded under %s\n", strings.Join(roots, ", "))
		return
	}
	// The ROOT column appears only when there is more than one to tell apart.
	// A single-root listing is the overwhelmingly common one, and a column
	// with the same value on every row is width spent on nothing.
	multi := len(roots) > 1
	// Column widths from the data, so a long app name does not ragged the
	// whole table and a short one does not waste half the terminal.
	wApp, wFlow, wRun, wRoot := len("APP"), len("FLOW"), len("RUN"), len("ROOT")
	for _, st := range list {
		wApp = max(wApp, len(st.App))
		wFlow = max(wFlow, len(st.Flow))
		wRun = max(wRun, len(st.RunID))
		wRoot = max(wRoot, len(st.Root))
	}
	rootCol := func(v string) string {
		if !multi {
			return ""
		}
		return fmt.Sprintf("%-*s  ", wRoot, v)
	}
	fmt.Fprintf(w, "  %s%-*s  %-*s  %-*s  %-9s  %s\n", rootCol("ROOT"), wApp, "APP", wFlow, "FLOW", wRun, "RUN", "STATE", "AGE")
	for _, st := range list {
		fmt.Fprintf(w, "%s %s%-*s  %-*s  %-*s  %-9s  %s\n",
			stateMark(st.State), rootCol(st.Root), wApp, st.App, wFlow, st.Flow, wRun, st.RunID,
			st.State, humanAge(st.AgeSeconds))
	}

	// The reason is printed only for the state that needs acting on.
	// Printing it for every row would bury the one line that matters.
	//
	// The root is named even in a single-root listing here, unlike in the
	// table: this line is the one someone copies into a bug report, and
	// "app/flow/run" alone does not say which tree to look in.
	for _, st := range list {
		if st.State == runs.StateAbandoned {
			fmt.Fprintf(w, "\n⚠ %s/%s/%s in %s — %s\n", st.App, st.Flow, st.RunID, st.Root, st.Reason)
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
