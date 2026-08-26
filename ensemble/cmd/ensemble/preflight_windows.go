//go:build windows

package main

import (
	"os/exec"
	"strconv"
	"syscall"
)

// preflightShell returns the argv runPreflightChecks executes one check's
// Run line with. Windows has no `/bin/sh`, so `cmd /C` stands in — same
// choice orchestrator's shellCommand makes for Service.Run (see
// proc_windows.go).
func preflightShell(run string) (path string, args []string) {
	return "cmd", []string{"/C", run}
}

// preflightConfigureKill gives cmd its own process group and points its
// context-cancel at `taskkill /T`, which walks that whole tree — Windows
// has no process groups you can signal directly, so this is the same
// best-effort emulation orchestrator's killProcessGroup uses, for the same
// reason (see proc_windows.go: killing only the immediate `cmd.exe` child
// would orphan whatever it launched).
func preflightConfigureKill(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
	cmd.Cancel = func() error {
		return exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
	}
}
