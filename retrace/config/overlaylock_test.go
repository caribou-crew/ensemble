//go:build unix

package config

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caribou-crew/ensemble/retrace/rules"
)

// TestHelperAppendsWireRules is the child half of
// TestNSeparateProcessesAppendingConcurrentlyLandNRules: this same test
// binary, re-executed as a genuinely separate OS process and filtered to
// this one function. It returns immediately (doing nothing) unless the
// parent selected it through the environment.
//
// Re-execing the test binary is how the parent gets REAL processes without
// building a throwaway command: overlayMu is a package-level mutex, so two
// goroutines in one process are serialized by construction and would prove
// nothing about the guarantee this test exists for.
func TestHelperAppendsWireRules(t *testing.T) {
	dir := os.Getenv("RETRACE_TEST_APPEND_DIR")
	if dir == "" {
		return
	}
	idx := os.Getenv("RETRACE_TEST_APPEND_IDX")
	n, err := strconv.Atoi(os.Getenv("RETRACE_TEST_APPEND_N"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "helper: bad RETRACE_TEST_APPEND_N:", err)
		os.Exit(9)
	}
	// Wait on the parent's starting gun so every process contends from the
	// same instant. Without it the children stagger by however long
	// exec.Start took, which is enough to hide the race on a fast machine.
	gun := os.Getenv("RETRACE_TEST_APPEND_GUN")
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); {
		if _, err := os.Stat(gun); err == nil {
			break
		}
		time.Sleep(200 * time.Microsecond)
	}
	for i := 0; i < n; i++ {
		r := rules.Raw{Path: "/p" + idx + "/rule" + strconv.Itoa(i), Headers: map[string]any{"date": "http-date"}}
		if err := AppendWireRule(dir, r); err != nil {
			fmt.Fprintln(os.Stderr, "helper: AppendWireRule:", err)
			os.Exit(9)
		}
	}
	os.Exit(0)
}

// TestNSeparateProcessesAppendingConcurrentlyLandNRules is the test Task 3
// could not write. Task 3 made AppendWireRule safe within one process (a
// mutex plus a same-directory temp-file/rename); readers were already safe
// across processes, because the rename is atomic. WRITERS were not: this
// task introduces the second writer process (`retrace ref rule` run in a
// terminal while the review server is open, or while a capture is in
// flight), and a lost append that returns a nil error is the same silent
// failure shape the atomicity fix was written to eliminate — just one level
// up.
//
// Every rule appended here is DISTINCT, so AppendWireRule's idempotent
// dedupe cannot account for a shortfall: N appends must land exactly N
// rules, and any missing one was lost by a read-modify-write race.
//
// unix-only, matching the lock (see overlaylock_unix.go): on a platform
// with no flock this would fail for a reason the code openly documents
// rather than for a defect.
func TestNSeparateProcessesAppendingConcurrentlyLandNRules(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping multi-process lock test in -short mode")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	const procs, per = 4, 25

	dir := t.TempDir()
	gun := filepath.Join(dir, "starting-gun")

	var wg sync.WaitGroup
	errs := make([]error, procs)
	out := make([]strings.Builder, procs)
	for p := 0; p < procs; p++ {
		cmd := exec.Command(self, "-test.run", "^TestHelperAppendsWireRules$")
		cmd.Env = append(os.Environ(),
			"RETRACE_TEST_APPEND_DIR="+dir,
			"RETRACE_TEST_APPEND_IDX="+strconv.Itoa(p),
			"RETRACE_TEST_APPEND_N="+strconv.Itoa(per),
			"RETRACE_TEST_APPEND_GUN="+gun,
		)
		cmd.Stdout, cmd.Stderr = &out[p], &out[p]
		if err := cmd.Start(); err != nil {
			t.Fatalf("starting child %d: %v", p, err)
		}
		wg.Add(1)
		go func(p int, cmd *exec.Cmd) {
			defer wg.Done()
			errs[p] = cmd.Wait()
		}(p, cmd)
	}
	if err := os.WriteFile(gun, []byte("go"), 0o644); err != nil {
		t.Fatalf("firing the starting gun: %v", err)
	}
	wg.Wait()
	for p, err := range errs {
		if err != nil {
			t.Fatalf("child %d failed: %v\n%s", p, err, out[p].String())
		}
	}

	got, err := readOverlay(filepath.Join(dir, OverlayPath))
	if err != nil {
		t.Fatalf("readOverlay: %v", err)
	}
	if len(got) != procs*per {
		t.Fatalf("overlay holds %d rules, want %d — %d appends were silently lost across %d processes, each returning a nil error",
			len(got), procs*per, procs*per-len(got), procs)
	}
	seen := map[string]bool{}
	for _, r := range got {
		if seen[r.Path] {
			t.Fatalf("overlay holds a duplicate rule for %q", r.Path)
		}
		seen[r.Path] = true
	}
}
