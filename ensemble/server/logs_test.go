package server_test

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeServiceLog drops content into the orchestrator's log dir as name's
// log file — the exact file the orchestrator itself writes and the log
// endpoints read.
func writeServiceLog(t *testing.T, e *testEnv, name, content string) string {
	t.Helper()
	path := filepath.Join(e.orch.LogDir(), name+".log")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func TestServiceLogsTail(t *testing.T) {
	e := newTestEnv(t)
	var lines []string
	for i := 1; i <= 10; i++ {
		lines = append(lines, fmt.Sprintf("line-%d", i))
	}
	writeServiceLog(t, e, "svc", strings.Join(lines, "\n")+"\n")

	resp, body := e.get(t, "/api/services/svc/logs?tail=3")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain", ct)
	}
	if want := "line-8\nline-9\nline-10\n"; string(body) != want {
		t.Errorf("tail=3 body = %q, want %q", body, want)
	}
}

func TestServiceLogsDefaultAndCap(t *testing.T) {
	e := newTestEnv(t)
	writeServiceLog(t, e, "svc", "a\nb\n")

	// No ?tail= — the 200-line default covers a 2-line file entirely.
	resp, body := e.get(t, "/api/services/svc/logs")
	if resp.StatusCode != http.StatusOK || string(body) != "a\nb\n" {
		t.Errorf("default tail: status=%d body=%q", resp.StatusCode, body)
	}
	// An absurd tail is capped, not an error.
	resp, body = e.get(t, "/api/services/svc/logs?tail=999999")
	if resp.StatusCode != http.StatusOK || string(body) != "a\nb\n" {
		t.Errorf("capped tail: status=%d body=%q", resp.StatusCode, body)
	}
}

func TestServiceLogsUnknownServiceIs404(t *testing.T) {
	e := newTestEnv(t)
	if resp, _ := e.get(t, "/api/services/nope/logs"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("logs: status = %d, want 404", resp.StatusCode)
	}
	if resp, _ := e.get(t, "/api/services/nope/logs/stream"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("stream: status = %d, want 404", resp.StatusCode)
	}
}

func TestServiceLogsNoFileIsEmptyNotError(t *testing.T) {
	e := newTestEnv(t)
	// Up already created svc.log for its "sleep 30" process — remove it to
	// simulate a service that hasn't produced a log file yet.
	if err := os.Remove(filepath.Join(e.orch.LogDir(), "svc.log")); err != nil {
		t.Fatalf("remove log: %v", err)
	}
	resp, body := e.get(t, "/api/services/svc/logs")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 for a missing log file", resp.StatusCode)
	}
	if len(body) != 0 {
		t.Errorf("body = %q, want empty", body)
	}
}

// TestServiceLogStreamReplaysTailThenFollows exercises the SSE follow:
// the initial frame replays the existing tail, and a line appended while
// connected arrives as a subsequent frame.
func TestServiceLogStreamReplaysTailThenFollows(t *testing.T) {
	e := newTestEnv(t)
	path := writeServiceLog(t, e, "svc", "old-1\nold-2\n")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.ts.URL+"/api/services/svc/logs/stream", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET stream: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q", ct)
	}

	sc := bufio.NewScanner(resp.Body)
	waitForDataLine := func(want string) {
		t.Helper()
		for sc.Scan() {
			if sc.Text() == "data: "+want {
				return
			}
		}
		t.Fatalf("stream ended before %q arrived: %v", want, sc.Err())
	}

	waitForDataLine("old-2")

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open for append: %v", err)
	}
	if _, err := f.WriteString("new-3\n"); err != nil {
		t.Fatalf("append: %v", err)
	}
	f.Close()

	waitForDataLine("new-3")
}
