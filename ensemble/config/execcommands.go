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
	// Steps is the command's steps, run in order. Copied to the clipboard
	// joined with " && ", so a failed step (e.g. no device attached) stops
	// the rest rather than running a later step against state the earlier
	// one never actually set up. Exactly one element across all of a
	// command's Steps is the literal sentinel "{{url}}" — see
	// TestExecCommandsHaveExactlyOneURLSentinel.
	Steps [][]string
	// ReversePorts marks a command as eligible for an entity link's
	// reverse: list (see EntityLink.Reverse), which prepends one
	// "adb reverse tcp:<port> tcp:<port>" step per named service/stub
	// before Steps — for an app running on a device or emulator to reach a
	// backend bound to the developer's own machine. Only meaningful for an
	// adb-based command: ios-simctl-openurl targets the Simulator, which
	// already shares the host's network stack, so there is nothing to
	// reverse a port to.
	ReversePorts bool
}

// execCommands is the closed table itself. Every entry must contain
// exactly one "{{url}}" sentinel across all its Steps — see
// TestExecCommandsHaveExactlyOneURLSentinel.
var execCommands = map[string]ExecCommand{
	"ios-simctl-openurl": {
		Steps: [][]string{{"xcrun", "simctl", "openurl", "booted", "{{url}}"}},
	},
	"adb-view": {
		Steps:        [][]string{{"adb", "shell", "am", "start", "-a", "android.intent.action.VIEW", "-d", "{{url}}"}},
		ReversePorts: true,
	},
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
