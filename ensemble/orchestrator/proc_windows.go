//go:build windows

package orchestrator

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

// Windows has no process groups you can signal and no signals to send, so
// the unix primitives are emulated:
//
//   - the shell is `cmd /C` rather than `sh -c`;
//   - the child gets CREATE_NEW_PROCESS_GROUP, which is what makes it (and
//     its own children) a killable tree rather than part of ensemble's;
//   - "signalling the group" is `taskkill /T`, which walks that tree.
//
// This is best-effort parity, not a claim of tested Windows support — it
// exists so the goreleaser matrix (which lists windows) builds, and so a
// Windows user gets a stack that starts and stops rather than a binary
// that was never compiled.

// shellCommand builds the command that runs a Service.Run line.
func shellCommand(run string) *exec.Cmd {
	return exec.Command("cmd", "/C", run)
}

// setProcessGroup makes the child the root of a new process group, so
// killProcessGroup's `taskkill /T` takes down everything it spawned
// instead of walking up into ensemble itself.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// killProcessGroup terminates the tree rooted at pid. sig picks how hard:
// anything other than SIGKILL asks politely first (`taskkill /T`, which
// only closes processes that cooperate), SIGKILL adds /F. A tree that's
// already gone is not an error — taskkill exits non-zero for "process not
// found", which is exactly the ESRCH case unix ignores, and there's no way
// to distinguish it from other failures short of parsing localized output,
// so a failed kill of a dead-or-dying tree is treated as success.
func killProcessGroup(pid int, sig syscall.Signal) error {
	if pid <= 0 {
		return nil
	}
	args := []string{"/T", "/PID", strconv.Itoa(pid)}
	if sig == syscall.SIGKILL {
		args = append([]string{"/F"}, args...)
	}
	if err := exec.Command("taskkill", args...).Run(); err != nil {
		if !processAlive(pid) {
			return nil
		}
		return err
	}
	return nil
}

// exitSignal is the unix reaper's signal-name lookup; Windows has no
// termination signals, so a finished process always reports "" and the
// reaper classifies purely on the exit code.
func exitSignal(_ *os.ProcessState) string { return "" }

// processAlive reports whether pid is still running, by asking the kernel
// for its exit code: a live process reports STILL_ACTIVE.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := syscall.OpenProcess(syscall.PROCESS_QUERY_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(h)

	const stillActive = 259 // STILL_ACTIVE
	var code uint32
	if err := syscall.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == stillActive
}
