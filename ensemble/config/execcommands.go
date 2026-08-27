package config

import "sort"

// ExecCommand is one entry in the closed, Go-authored table of local CLI
// commands a "kind: exec" entity link can reference by name (see
// EntityLink.Exec). Not config-extensible: ensemble.yaml is a committed
// file that travels between repos and machines, and a config key naming an
// arbitrary binary would mean a PR editing that file could put a command
// of its author's choosing on a teammate's clipboard, one paste away from
// running. Adding a command here is a Go change and a code review.
type ExecCommand struct {
	// Argv is the command's argument vector. Exactly one element is the
	// literal sentinel "{{url}}", marking the slot the caller substitutes
	// the resolved (and shell-quoted, on the client) URL into.
	Argv []string
}

// execCommands is the closed table itself. Every entry must contain
// exactly one "{{url}}" sentinel — see TestExecCommandsHaveExactlyOneURLSentinel.
var execCommands = map[string]ExecCommand{
	"ios-simctl-openurl": {Argv: []string{"xcrun", "simctl", "openurl", "booted", "{{url}}"}},
	"adb-view":           {Argv: []string{"adb", "shell", "am", "start", "-a", "android.intent.action.VIEW", "-d", "{{url}}"}},
}

// LookupExecCommand returns the named command and whether it exists.
// config/validate.go and server/entities.go are the only legitimate
// callers — a lookup function, rather than exporting the map directly,
// keeps "known name" defined in exactly one place.
func LookupExecCommand(name string) (ExecCommand, bool) {
	cmd, ok := execCommands[name]
	return cmd, ok
}

// ExecCommandNames returns every valid EntityLink.Exec name, sorted —
// for validation errors that must list what's actually available.
func ExecCommandNames() []string {
	names := make([]string, 0, len(execCommands))
	for name := range execCommands {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
