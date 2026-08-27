package config

import (
	"slices"
	"strings"
	"testing"
)

// TestExecCommandsHaveExactlyOneURLSentinel guards execCommands' one real
// invariant: a table entry with zero "{{url}}" elements would produce a
// command that never receives the resolved URL, and one with two would
// substitute it into the wrong slot — both are bugs in the table, not
// something a caller can catch, so this must fail here rather than surface
// as a dashboard oddity.
func TestExecCommandsHaveExactlyOneURLSentinel(t *testing.T) {
	if len(execCommands) == 0 {
		t.Fatal("execCommands is empty")
	}
	for name, cmd := range execCommands {
		count := 0
		for _, arg := range cmd.Argv {
			if arg == "{{url}}" {
				count++
			}
		}
		if count != 1 {
			t.Errorf("exec command %q: argv has %d {{url}} sentinels, want exactly 1 (argv=%v)", name, count, cmd.Argv)
		}
	}
}

func TestLookupExecCommand(t *testing.T) {
	if _, ok := LookupExecCommand("adb-view"); !ok {
		t.Error("adb-view should be a known command")
	}
	if _, ok := LookupExecCommand("does-not-exist"); ok {
		t.Error("does-not-exist should not be a known command")
	}
}

func TestExecCommandNamesListsEveryEntry(t *testing.T) {
	names := ExecCommandNames()
	if len(names) != len(execCommands) {
		t.Fatalf("ExecCommandNames() = %v, want %d entries", names, len(execCommands))
	}
	for name := range execCommands {
		if !slices.Contains(names, name) {
			t.Errorf("ExecCommandNames() missing %q", name)
		}
	}
	// Sorted, so an error message listing them is stable and readable.
	joined := strings.Join(names, ", ")
	for i := 1; i < len(names); i++ {
		if names[i-1] > names[i] {
			t.Errorf("ExecCommandNames() not sorted: %s", joined)
			break
		}
	}
}
