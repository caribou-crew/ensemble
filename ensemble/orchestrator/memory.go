package orchestrator

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// memSampleTimeout bounds a single `ps`/`docker stats` shell-out so one
// stuck sample (e.g. a docker daemon that's wedged) can't hold up the
// whole WithMemory call, or by extension GET /api/status.
const memSampleTimeout = 2 * time.Second

// WithMemory returns a copy of states with RSSKB best-effort filled in for
// every native node with a live PID and every docker-placed node, sampled
// concurrently (one `ps`/`docker stats` per node, not one call covering
// all of them — there's no single command that reports both). A node
// whose sample fails (already exited, docker unreachable, ...) is simply
// left at RSSKB 0 rather than failing the whole call — memory is a
// best-effort display value, not something callers should have to handle
// errors for.
func (o *Orchestrator) WithMemory(ctx context.Context, states []ServiceState) []ServiceState {
	out := make([]ServiceState, len(states))
	copy(out, states)

	var wg sync.WaitGroup
	for i := range out {
		s := &out[i]
		switch {
		case s.Placement == "native" && s.PID > 0:
			wg.Add(1)
			go func(s *ServiceState) {
				defer wg.Done()
				if kb, err := sampleNativeRSSKB(ctx, s.PID); err == nil {
					s.RSSKB = kb
				}
			}(s)
		case s.Placement == "docker":
			wg.Add(1)
			go func(s *ServiceState) {
				defer wg.Done()
				if kb, err := sampleDockerRSSKB(ctx, s.Name); err == nil {
					s.RSSKB = kb
				}
			}(s)
		}
	}
	wg.Wait()
	return out
}

// sampleNativeRSSKB reads pid's resident set size via `ps`, which reports
// RSS in KB on both macOS and Linux — avoids the /proc-vs-BSD split
// proc_unix.go/proc_windows.go needs for process control.
func sampleNativeRSSKB(ctx context.Context, pid int) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, memSampleTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-o", "rss=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0, fmt.Errorf("ps -p %d: %w", pid, err)
	}
	kb, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse ps rss output %q: %w", out, err)
	}
	return kb, nil
}

// sampleDockerRSSKB reads name's container's current memory usage via
// `docker stats`, parsing the "used" half of MemUsage's "12.5MiB / 2GiB".
func sampleDockerRSSKB(ctx context.Context, name string) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, memSampleTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "stats", "--no-stream", "--format", "{{.MemUsage}}", dockerContainerName(name)).Output()
	if err != nil {
		return 0, fmt.Errorf("docker stats %s: %w", dockerContainerName(name), err)
	}
	used, _, ok := strings.Cut(strings.TrimSpace(string(out)), " / ")
	if !ok {
		return 0, fmt.Errorf("unexpected docker stats MemUsage format %q", out)
	}
	return parseMemSizeKB(used)
}

// parseMemSizeKB parses a docker-style size like "12.5MiB", "512KiB",
// "1.2GiB", or "800B" into KB. Docker's stats formatter always uses the
// binary (Ki/Mi/Gi) units, but B/K/M/G is accepted too for robustness
// against a future docker CLI output change.
func parseMemSizeKB(s string) (int64, error) {
	i := 0
	for i < len(s) && (s[i] == '.' || (s[i] >= '0' && s[i] <= '9')) {
		i++
	}
	if i == 0 {
		return 0, fmt.Errorf("no numeric prefix in %q", s)
	}
	value, err := strconv.ParseFloat(s[:i], 64)
	if err != nil {
		return 0, fmt.Errorf("parse numeric prefix %q: %w", s[:i], err)
	}

	rest := s[i:]
	var unit string
	switch {
	case strings.HasSuffix(rest, "iB"):
		unit = strings.TrimSuffix(rest, "iB") // "MiB" -> "M"
	case strings.HasSuffix(rest, "B"):
		unit = strings.TrimSuffix(rest, "B") // "B" -> "" (bytes); "MB" -> "M"
	default:
		unit = rest
	}

	var kb float64
	switch strings.ToUpper(unit) {
	case "":
		kb = value / 1024 // bytes -> KB
	case "K":
		kb = value
	case "M":
		kb = value * 1024
	case "G":
		kb = value * 1024 * 1024
	case "T":
		kb = value * 1024 * 1024 * 1024
	default:
		return 0, fmt.Errorf("unknown unit %q in %q", unit, s)
	}
	return int64(kb), nil
}
