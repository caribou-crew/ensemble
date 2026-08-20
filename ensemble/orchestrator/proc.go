package orchestrator

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// homeDir wraps os.UserHomeDir so resolveDir can be tested and so a lookup
// failure degrades gracefully (leaves the leading ~ untouched) instead of
// panicking.
func homeDir() (string, error) {
	return os.UserHomeDir()
}

// resolveDir resolves a Service.Dir relative to base (Config.Dir),
// expanding a leading "~" to the user's home directory. An empty dir
// resolves to base itself.
func resolveDir(base, dir string) string {
	if dir == "" {
		return base
	}
	if dir == "~" || strings.HasPrefix(dir, "~/") {
		if home, err := homeDir(); err == nil {
			dir = filepath.Join(home, strings.TrimPrefix(dir, "~"))
		}
	}
	if filepath.IsAbs(dir) {
		return dir
	}
	return filepath.Join(base, dir)
}

// envSlice flattens a Service.Env map into "K=V" entries, appended to
// os.Environ() so a service inherits the ensemble process's environment
// plus its own overrides.
func envSlice(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

// startNativeProcess launches run via `/bin/sh -c run` in workDir, its own
// process group (so killProcessGroup can take down shell children too),
// with stdout/stderr appended to logPath.
func startNativeProcess(run, workDir string, env map[string]string, logPath string) (*exec.Cmd, error) {
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return nil, fmt.Errorf("create log dir: %w", err)
	}
	logFile, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open log %s: %w", logPath, err)
	}

	cmd := exec.Command("/bin/sh", "-c", run)
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(), envSlice(env)...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		logFile.Close()
		return nil, fmt.Errorf("start %q: %w", run, err)
	}
	// Wait must be called to avoid a zombie once the process exits; the log
	// file is only safe to close once nothing can write to it anymore.
	go func() {
		_ = cmd.Wait()
		logFile.Close()
	}()
	return cmd, nil
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
