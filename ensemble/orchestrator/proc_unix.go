//go:build !windows

package orchestrator

import (
	"os/exec"
	"syscall"
)

// shellCommand builds the command that runs a Service.Run line. `sh -c`
// so the config can use pipes, `&&`, and env expansion the way a developer
// would type it into a terminal.
func shellCommand(run string) *exec.Cmd {
	return exec.Command("/bin/sh", "-c", run)
}

// setProcessGroup puts the shell in its own process group, so
// killProcessGroup's negative-pid signal reaches the children it spawned
// (a `sh -c "npm start"` is a shell whose real server is a grandchild —
// signalling only the shell would orphan it).
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup signals the process group led by pid (negative pid
// targets the whole group), so shell children spawned by Service.Run die
// along with the shell itself. A group that's already gone is not an
// error.
func killProcessGroup(pid int, sig syscall.Signal) error {
	if pid <= 0 {
		return nil
	}
	err := syscall.Kill(-pid, sig)
	if err != nil && err == syscall.ESRCH {
		return nil
	}
	return err
}

// processAlive reports whether pid is still alive, via a signal-0 probe.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}
