package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/caribou-crew/ensemble/ensemble/config"
)

// Build output must reach the service log while the build is still
// running — not only once it exits.
func TestRunBuildStreamsToLog(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "svc.log")
	done := make(chan error, 1)
	go func() {
		done <- runBuild("echo first; sleep 2; echo second", dir, logPath)
	}()
	// The header line quotes the command, so look for the echoed lines
	// themselves (newline-delimited), not the bare words.
	deadline := time.Now().Add(1500 * time.Millisecond)
	for {
		b, _ := os.ReadFile(logPath)
		if strings.Contains(string(b), "\nfirst\n") {
			if strings.Contains(string(b), "\nsecond\n") {
				t.Fatal("'second' should not have been printed yet")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("'first' not streamed before the build finished; log = %q", b)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(logPath)
	for _, want := range []string{"=== build: echo first", "first\nsecond", "=== build ok ==="} {
		if !strings.Contains(string(b), want) {
			t.Errorf("log lacks %q:\n%s", want, b)
		}
	}
}

func TestRunBuildFailureCarriesOutputTail(t *testing.T) {
	dir := t.TempDir()
	err := runBuild("echo 'reason: base image pull denied' >&2; exit 3", dir, filepath.Join(dir, "svc.log"))
	if err == nil || !strings.Contains(err.Error(), "base image pull denied") || !strings.Contains(err.Error(), "exit status 3") {
		t.Fatalf("err = %v", err)
	}
	// The tail is bounded.
	// (Generate the output in the shell: the command itself is quoted into
	// the error, so a 10 KB command would defeat the point of the test.)
	err = runBuild("head -c 10240 /dev/zero | tr '\\0' x; exit 1", dir, filepath.Join(dir, "svc.log"))
	if err == nil || len(err.Error()) > buildTailBytes+200 {
		t.Fatalf("tail not bounded: %d bytes", len(err.Error()))
	}
}

// End to end: a failing build marks the service failed with the reason.
func TestUpFailedBuildReasonInLastErr(t *testing.T) {
	cfg := &config.Config{
		Dir: t.TempDir(),
		Services: map[string]config.Service{
			"svc": {Run: "sleep 30", Build: "echo 'gradle: cannot resolve artifactory' ; exit 1"},
		},
	}
	o := newTestOrchestrator(t, cfg, Opts{})
	err := o.Up(context.Background())
	if err == nil || !strings.Contains(err.Error(), "artifactory") {
		t.Fatalf("Up err = %v", err)
	}
	st, _ := o.Service("svc")
	if st.Status != StatusFailed || !strings.Contains(st.LastErr, "artifactory") {
		t.Fatalf("state = %+v", st)
	}
	b, _ := os.ReadFile(filepath.Join(o.opts.LogDir, "svc.log"))
	if !strings.Contains(string(b), "artifactory") || !strings.Contains(string(b), "=== build failed") {
		t.Errorf("service log lacks the build output:\n%s", b)
	}
}

func TestDockerRunServiceArgsPassthrough(t *testing.T) {
	got := dockerRunServiceArgs("mono", &config.DockerPlacement{
		Image: "monolith:local",
		Ports: []string{"8081:8080"},
		Env:   map[string]string{"B": "2", "A": "1"},
		Args:  []string{"--add-host=host.docker.internal:host-gateway", "--platform", "linux/amd64"},
	})
	want := []string{"run", "-d", "--name", "ensemble-mono", "-p", "8081:8080", "-e", "A=1", "-e", "B=2",
		"--add-host=host.docker.internal:host-gateway", "--platform", "linux/amd64", "monolith:local"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %v\nwant %v", got, want)
	}
}
