package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/caribou-crew/ensemble/retrace/suites"
)

const suiteUsage = "retrace suite import --file FILE [--json]"

func cmdSuite(args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Fprintln(stdout, suiteUsage)
		return exitOK
	}
	if len(args) == 0 || args[0] != "import" {
		return fail(stderr, "usage: %s", suiteUsage)
	}
	fs := flag.NewFlagSet("suite import", flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("file", "", "runner report JSON file")
	asJSON := fs.Bool("json", false, "emit imported report as JSON")
	if err := fs.Parse(args[1:]); err != nil {
		return exitUsage
	}
	if *file == "" || fs.NArg() != 0 {
		return fail(stderr, "usage: %s", suiteUsage)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return fail(stderr, "suite import: %v", err)
	}
	attempt, err := suites.Import(cwd, *file)
	if err != nil {
		return fail(stderr, "suite import: %v", err)
	}
	if *asJSON {
		if err := writeJSON(stdout, attempt); err != nil {
			return fail(stderr, "suite import: %v", err)
		}
	} else {
		fmt.Fprintf(stdout, "Imported suite %s attempt %s.\n", attempt.SuiteID, attempt.AttemptID)
	}
	return exitOK
}
