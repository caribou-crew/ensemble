//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

// preflightShell returns the argv runPreflightChecks executes one check's
// Run line with — the same `/bin/sh -c` convention as the rest of ensemble
// (Service.Run, on_ready, seeds).
func preflightShell(run string) (path string, args []string) {
	return "/bin/sh", []string{"-c", run}
}

// preflightConfigureKill puts cmd in its own process group and points its
// context-cancel at killing that whole group, not just the shell —
// mirrors orchestrator's setProcessGroup/killProcessGroup for the same
// reason (see proc_unix.go). Without this, a check that times out but has
// already forked a child (`sh -c "sleep 30"` on Linux actually forks,
// unlike some shells' tail-call exec optimization for a single simple
// command) leaves the shell killed but its child running — and
// CombinedOutput blocks reading the shared stdout/stderr pipe until that
// orphaned child exits on its own, silently defeating the timeout.
func preflightConfigureKill(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
