package server

// Service log endpoints: read-only GETs over the per-service log files the
// orchestrator already writes (.ensemble/run/<name>.log — build output,
// hook sections, and the process's own stdout/stderr). GET .../logs
// returns a plain-text tail; GET .../logs/stream follows the file over SSE
// (an initial tail, then appended lines), same framing conventions as
// handleTrafficStream.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	logTailDefault = 200
	logTailCap     = 5000
	// logStreamPollInterval is how often the SSE follow re-stats the file
	// for growth — a poll, not fsnotify, to stay dependency-free; 250ms
	// matches the health poll's idea of "feels live".
	logStreamPollInterval = 250 * time.Millisecond
	// logStreamMaxChunk bounds how much a single tick reads and emits, so
	// a burst (a build dumping megabytes) streams in bounded pieces
	// instead of one giant allocation and SSE frame.
	logStreamMaxChunk = 256 * 1024
)

// serviceLogPath is where the orchestrator logs name's output — the same
// path startServiceAs/runShellStep write.
func (s *server) serviceLogPath(name string) string {
	return filepath.Join(s.Orch.LogDir(), name+".log")
}

// knownService 404s for a name the config has no service for. Databases
// and stubs are excluded on purpose: neither writes a log file here
// (databases log to docker, stubs are in-process).
func (s *server) knownService(w http.ResponseWriter, name string) bool {
	if _, ok := s.Cfg.Services[name]; ok {
		return true
	}
	writeErr(w, http.StatusNotFound, fmt.Sprintf("service %q not found", name))
	return false
}

// handleServiceLogs serves GET /api/services/{name}/logs?tail=N: the last
// N lines (default logTailDefault, capped at logTailCap) of the service's
// log file as plain text. A service with no log file yet returns an empty
// 200, not an error — "no output so far" is a normal answer.
func (s *server) handleServiceLogs(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !s.knownService(w, name) {
		return
	}
	tail := parseInt(r.URL.Query().Get("tail"))
	if tail <= 0 {
		tail = logTailDefault
	}
	tail = min(tail, logTailCap)

	lines, err := tailLines(s.serviceLogPath(name), tail)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(lines)
}

// tailLines returns the last n lines of the file at path (keeping the
// trailing newline when the file ends with one), reading backwards in
// chunks so a rotated-log-sized file isn't slurped whole for a 200-line
// tail. Returns the os.ErrNotExist from Open unchanged so callers can
// treat "no log yet" specially.
func tailLines(path string, n int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}

	const chunk = 32 * 1024
	var buf []byte
	offset := info.Size()
	for offset > 0 && !hasLines(buf, n) {
		readLen := min(offset, int64(chunk))
		offset -= readLen
		part := make([]byte, readLen)
		if _, err := f.ReadAt(part, offset); err != nil {
			return nil, err
		}
		buf = append(part, buf...)
	}
	return lastLines(buf, n), nil
}

// hasLines reports whether b already holds at least n complete lines plus
// the boundary before them — i.e. n newlines, not counting one that merely
// terminates the final line.
func hasLines(b []byte, n int) bool {
	if len(b) > 0 && b[len(b)-1] == '\n' {
		b = b[:len(b)-1]
	}
	return bytes.Count(b, []byte{'\n'}) >= n
}

// lastLines trims b to its last n lines.
func lastLines(b []byte, n int) []byte {
	trimmed := b
	if len(trimmed) > 0 && trimmed[len(trimmed)-1] == '\n' {
		trimmed = trimmed[:len(trimmed)-1]
	}
	lines := bytes.Count(trimmed, []byte{'\n'}) + 1
	idx := 0
	for i := 0; i < lines-n; i++ {
		idx += bytes.IndexByte(trimmed[idx:], '\n') + 1
	}
	return b[idx:]
}

// handleServiceLogStream serves GET /api/services/{name}/logs/stream: an
// SSE follow of the service's log — one "log" event replaying the current
// tail (logTailDefault lines), then an event per appended chunk of
// complete lines, until the client disconnects. A file that doesn't exist
// yet simply produces no events until it does; rotation (the file
// shrinking) restarts the follow from the top of the new file.
func (s *server) handleServiceLogStream(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !s.knownService(w, name) {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming not supported")
		return
	}
	path := s.serviceLogPath(name)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// Replay the current tail so a client doesn't need a second endpoint
	// to show context, then follow from the file's current end.
	var offset int64
	if b, err := tailLines(path, logTailDefault); err == nil {
		if err := writeLogEvent(w, b); err != nil {
			return
		}
		flusher.Flush()
		if info, err := os.Stat(path); err == nil {
			offset = info.Size()
		}
	}

	ticker := time.NewTicker(logStreamPollInterval)
	defer ticker.Stop()
	heartbeat := time.NewTicker(heartbeatInterval)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-ticker.C:
			info, err := os.Stat(path)
			if err != nil {
				continue // not created yet, or rotated away mid-poll
			}
			if info.Size() < offset {
				offset = 0 // rotation: a fresh file started
			}
			if info.Size() == offset {
				continue
			}
			chunk, n, err := readLogChunk(path, offset)
			if err != nil || n == 0 {
				continue
			}
			// Emit only complete lines; a partial line waits for its
			// newline (unless the chunk is at the size cap — then it goes
			// out as-is rather than stalling forever on one huge line).
			idx := bytes.LastIndexByte(chunk[:n], '\n')
			if idx < 0 {
				if n < logStreamMaxChunk {
					continue
				}
				idx = n - 1
			}
			if err := writeLogEvent(w, chunk[:idx+1]); err != nil {
				return
			}
			flusher.Flush()
			offset += int64(idx + 1)
		}
	}
}

// readLogChunk reads up to logStreamMaxChunk bytes of path starting at
// offset.
func readLogChunk(path string, offset int64) ([]byte, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	chunk := make([]byte, logStreamMaxChunk)
	n, err := f.ReadAt(chunk, offset)
	if n == 0 && err != nil && err != io.EOF {
		return nil, 0, err
	}
	return chunk, n, nil
}

// writeLogEvent frames raw log bytes as one SSE "log" event. SSE data
// cannot carry raw newlines, so each line becomes its own data: field —
// EventSource joins them back with "\n" on delivery.
func writeLogEvent(w io.Writer, b []byte) error {
	if len(b) == 0 {
		return nil
	}
	var sb strings.Builder
	sb.WriteString("event: log\n")
	for line := range strings.SplitSeq(strings.TrimSuffix(string(b), "\n"), "\n") {
		sb.WriteString("data: ")
		sb.WriteString(line)
		sb.WriteString("\n")
	}
	sb.WriteString("\n")
	_, err := io.WriteString(w, sb.String())
	return err
}
