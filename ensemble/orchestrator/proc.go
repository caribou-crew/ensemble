package orchestrator

import (
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/caribou-crew/ensemble/core/trace"
)

// The process-control primitives startNativeProcess builds on —
// shellCommand, setProcessGroup, killProcessGroup, processAlive — are
// platform-specific and live in proc_unix.go / proc_windows.go. Everything
// in this file is shared.

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

// envSlice merges env into the ensemble process's own environment, config
// entries winning on key collisions, and flattens the result into "K=V"
// entries. A plain append of env after os.Environ() doesn't reliably
// override: most libc getenv() implementations return the first match for
// a duplicate key, so a config override placed after the parent's own
// entry for the same name would be shadowed by it in the child.
func envSlice(env map[string]string) []string {
	merged := make(map[string]string, len(env))
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			merged[k] = v
		}
	}
	maps.Copy(merged, env)
	out := make([]string, 0, len(merged))
	for k, v := range merged {
		out = append(out, k+"="+v)
	}
	return out
}

// startNativeProcess launches run through the platform's shell
// (shellCommand) in workDir, in its own process group (so killProcessGroup
// can take down shell children too), with stdout/stderr appended to
// logPath. logPath is a rotating file (see serviceLogMaxBytes) rather than
// a plain append: a long-running dev server, left up for days or stuck in a
// noisy retry loop, would otherwise grow it without bound.
func startNativeProcess(run, workDir string, env map[string]string, logPath string) (*exec.Cmd, error) {
	// 0700/0600 (set by OpenRotatingFile): a service's stdout/stderr
	// routinely carries connection strings, tokens, and request payloads —
	// same reasoning as the hop log's permissions in cmd/ensemble's runUp.
	logFile, err := trace.OpenRotatingFile(logPath, serviceLogMaxBytes, serviceLogKeep)
	if err != nil {
		return nil, fmt.Errorf("open log %s: %w", logPath, err)
	}

	cmd := shellCommand(run)
	cmd.Dir = workDir
	cmd.Env = envSlice(env)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	setProcessGroup(cmd)

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
