package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/caribou-crew/ensemble/ensemble/config"
)

// A database container is the one thing in a stack that is expensive to
// recreate and usually wanted intact, so `up` after `up` adopts an existing
// ensemble-<name> container instead of failing on it. Three states, one
// convergence point — these tests pin all three, plus the two ways the
// decision can go wrong in a way no one would notice:
//
//   - absent            -> docker run          (and NOT docker start)
//   - exists, running   -> neither run nor start; straight to the health gate
//   - exists, stopped   -> docker start        (and NOT docker run)
//
// Before this, startDatabase had no unit coverage at all: every database test
// in the package was docker-gated, so it skipped on any machine without a
// daemon — including CI. The whole create/adopt/restart decision could have
// been wrong in either direction and the suite would have stayed green.

// dockerScript writes a fake `docker` onto PATH that logs every invocation and
// answers `inspect` from a state file. It exercises the real exec path in
// docker.go — argv included — rather than stubbing the decision out.
//
// The state file is what makes this a fair fake rather than a fixed oracle:
// `run` and `start` move the container to running, exactly as docker does, so
// the health gate that follows (pollDockerRunning re-inspects) sees the
// consequence of the decision under test. A fake that answered `inspect` the
// same way forever would let a create path "succeed" while the container it
// claims to have started stays absent.
//
// state is one of "absent", "running", "stopped". "absent" reproduces docker's
// actual failure shape for a missing container: exit 1 with "No such object",
// which is what dockerContainerState has to tell apart from a broken daemon.
func dockerScript(t *testing.T, state string) (logPath string) {
	t.Helper()
	switch state {
	case "absent", "running", "stopped":
	default:
		t.Fatalf("unknown state %q", state)
	}
	binDir := t.TempDir()
	logPath = filepath.Join(binDir, "docker-calls.log")
	statePath := filepath.Join(binDir, "container-state")
	if err := os.WriteFile(statePath, []byte(state+"\n"), 0o644); err != nil {
		t.Fatalf("write container state: %v", err)
	}

	script := `#!/bin/sh
echo "$@" >> "` + logPath + `"
state=$(cat "` + statePath + `")
case "$1" in
  inspect)
    case "$state" in
      absent)  echo "Error: No such object: $4" >&2; exit 1 ;;
      running) echo true ;;
      stopped) echo false ;;
    esac ;;
  run)   echo running > "` + statePath + `"; echo fakecontainerid ;;
  start) echo running > "` + statePath + `"; echo "$2" ;;
  rm)    echo absent > "` + statePath + `" ;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(binDir, "docker"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake docker: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

// dockerCalls returns the fake's log as one line per invocation.
func dockerCalls(t *testing.T, logPath string) []string {
	t.Helper()
	b, err := os.ReadFile(logPath)
	if err != nil {
		return nil // never invoked at all
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// calledVerb reports whether docker was invoked with the given first argument.
func calledVerb(calls []string, verb string) bool {
	for _, c := range calls {
		if strings.HasPrefix(c, verb+" ") || c == verb {
			return true
		}
	}
	return false
}

func dbConfig(t *testing.T) *config.Config {
	t.Helper()
	return &config.Config{
		Dir: t.TempDir(),
		Databases: map[string]config.Database{
			"db": {Type: "postgres", Image: "postgres:16", Port: 55999},
		},
	}
}

// startDatabaseForTest runs just the database half of Up, with the readiness
// check stubbed: these tests are about the create/adopt/restart decision, and
// a real DBReady would need a real postgres. DBReady is the orchestrator's own
// injection point (see the field comment), not a seam invented for the test.
func startDatabaseForTest(t *testing.T, cfg *config.Config) error {
	t.Helper()
	// Short gate budget: with a correct decision the fake is ready on the
	// first poll, so this only bounds how long a *wrong* decision takes to
	// report — 5s instead of the 30s default.
	o := newTestOrchestrator(t, cfg, Opts{HealthTimeout: 5 * time.Second})
	o.DBReady = func(context.Context, string, config.Database) error { return nil }
	return o.startDatabase(context.Background(), "db", cfg.Databases["db"])
}

func TestStartDatabaseCreatesWhenNoContainerExists(t *testing.T) {
	logPath := dockerScript(t, "absent")
	if err := startDatabaseForTest(t, dbConfig(t)); err != nil {
		t.Fatalf("startDatabase: %v", err)
	}
	calls := dockerCalls(t, logPath)

	if !calledVerb(calls, "run") {
		t.Errorf("no container existed, so it must be created; got calls: %v", calls)
	}
	// The half that matters and would otherwise pass silently: an absent
	// container must never be "started". If dockerContainerState ever reports
	// a missing container as existing-but-stopped, `docker start` fails on a
	// container that was never created and the create path is dead.
	if calledVerb(calls, "start") {
		t.Errorf("nothing existed to start, but docker start was called; got calls: %v", calls)
	}
}

func TestStartDatabaseAdoptsAnAlreadyRunningContainer(t *testing.T) {
	logPath := dockerScript(t, "running")
	if err := startDatabaseForTest(t, dbConfig(t)); err != nil {
		t.Fatalf("startDatabase: %v", err)
	}
	calls := dockerCalls(t, logPath)

	// This is the whole point of the feature: a second `up` must not try to
	// create a container that is already there (docker would fail the name
	// conflict) nor restart one that is already serving.
	if calledVerb(calls, "run") {
		t.Errorf("a running container must be adopted, not recreated; got calls: %v", calls)
	}
	if calledVerb(calls, "start") {
		t.Errorf("a running container must not be restarted; got calls: %v", calls)
	}
	if !calledVerb(calls, "inspect") {
		t.Errorf("the decision must be made by inspecting the container; got calls: %v", calls)
	}
}

func TestStartDatabaseRestartsAnExistingStoppedContainer(t *testing.T) {
	logPath := dockerScript(t, "stopped")
	if err := startDatabaseForTest(t, dbConfig(t)); err != nil {
		t.Fatalf("startDatabase: %v", err)
	}
	calls := dockerCalls(t, logPath)

	if !calledVerb(calls, "start") {
		t.Errorf("a stopped container must be started; got calls: %v", calls)
	}
	// Recreating instead of starting is the data-loss direction: `docker run`
	// on an existing name fails, and "fixing" that with rm -f would silently
	// discard the volume the developer wanted kept.
	if calledVerb(calls, "run") {
		t.Errorf("a stopped container must be started, never recreated; got calls: %v", calls)
	}
}

// The container ensemble adopts is only ever its own. Nothing here should
// address a bare service name — a developer's own "db" container is not
// ensemble's to inspect, start, or remove.
func TestStartDatabaseOnlyEverAddressesItsOwnContainerName(t *testing.T) {
	logPath := dockerScript(t, "stopped")
	if err := startDatabaseForTest(t, dbConfig(t)); err != nil {
		t.Fatalf("startDatabase: %v", err)
	}
	want := dockerContainerName("db")
	for _, c := range dockerCalls(t, logPath) {
		if !strings.Contains(c, want) {
			t.Errorf("docker call %q does not address %s — ensemble must only touch its own containers", c, want)
		}
	}
}

// A broken daemon must NOT be read as "no container here". If it were,
// startDatabase would fall through to `docker run` and report a confusing
// create failure instead of the infrastructure problem that actually stopped
// it — and on a wedged daemon it would do so after `run` hung too.
func TestDockerContainerStateDistinguishesAbsentFromBroken(t *testing.T) {
	for _, tc := range []struct {
		name      string
		stderr    string
		exit      int
		wantErr   bool
		wantExist bool
	}{
		{name: "absent", stderr: "Error: No such object: ensemble-db", exit: 1},
		{name: "absent podman wording", stderr: "Error: no such container ensemble-db", exit: 1},
		{name: "daemon down", stderr: "Cannot connect to the Docker daemon", exit: 1, wantErr: true},
		{name: "permission denied", stderr: "permission denied while trying to connect", exit: 1, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			binDir := t.TempDir()
			script := fmt.Sprintf("#!/bin/sh\necho %q >&2\nexit %d\n", tc.stderr, tc.exit)
			if err := os.WriteFile(filepath.Join(binDir, "docker"), []byte(script), 0o755); err != nil {
				t.Fatalf("write fake docker: %v", err)
			}
			t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

			exists, _, err := dockerContainerState(context.Background(), "db")
			if tc.wantErr {
				if err == nil {
					t.Fatalf("a broken daemon (%q) must be an error, not a silent 'no container'", tc.stderr)
				}
				return
			}
			if err != nil {
				t.Fatalf("a missing container is a normal answer, not an error: %v", err)
			}
			if exists != tc.wantExist {
				t.Errorf("exists = %v, want %v", exists, tc.wantExist)
			}
		})
	}
}

// The lookup runs inside `ensemble up`. A wedged daemon does not fail
// `inspect`, it blocks — so without ctx this hangs the tool a developer runs
// most. Proven with a `docker` that never returns.
func TestDockerContainerStateIsBoundedByContext(t *testing.T) {
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "docker"), []byte("#!/bin/sh\nsleep 600\n"), 0o755); err != nil {
		t.Fatalf("write fake docker: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, _, err := dockerContainerState(ctx, "db")
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a wedged daemon must surface as an error, not a clean 'no container'")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("dockerContainerState ignored its context and hung on a wedged daemon")
	}
}
