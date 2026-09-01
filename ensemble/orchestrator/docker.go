package orchestrator

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/caribou-crew/ensemble/ensemble/config"
)

// dockerContainerName is the always-applied naming convention: every
// container ensemble manages is prefixed "ensemble-" so it's identifiable
// (and safely rm -f'able) alongside unrelated containers on the host.
func dockerContainerName(name string) string {
	return "ensemble-" + name
}

// dockerRunService starts a Service's docker placement: `docker run -d
// --name ensemble-<name> -p <ports> -e K=V <image>`.
func dockerRunService(name string, d *config.DockerPlacement) error {
	return runDocker(name, dockerRunServiceArgs(name, d))
}

// dockerRunServiceArgs builds the `docker run` argv for a service
// placement: ports, env, then d.Args verbatim, then the image. Env keys
// are emitted sorted so the argv is stable.
func dockerRunServiceArgs(name string, d *config.DockerPlacement) []string {
	args := []string{"run", "-d", "--name", dockerContainerName(name)}
	for _, p := range d.Ports {
		args = append(args, "-p", p)
	}
	keys := make([]string, 0, len(d.Env))
	for k := range d.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, "-e", fmt.Sprintf("%s=%s", k, d.Env[k]))
	}
	args = append(args, d.Args...)
	args = append(args, d.Image)
	return args
}

// defaultContainerPorts maps a managed database's Type to the port its
// image listens on by default *inside* the container — independent of
// db.Port, which is the HOST port ensemble publishes it under. Every
// managed image ensemble knows about listens on its own fixed port
// regardless of what host port the user picks (task 3.6, defect D2):
// publishing host db.Port to container db.Port is dead on arrival for any
// non-default db.Port, since nothing inside the container listens there.
var defaultContainerPorts = map[string]int{
	"postgres":   5432,
	"mysql":      3306,
	"redis":      6379,
	"dynamodb":   8000,
	"localstack": 4566,
}

// resolveContainerPort picks db's container-side port: db.ContainerPort
// when explicitly set (the config escape hatch for an image on a
// non-default port), else the defaultContainerPorts entry for db.Type,
// else db.Port itself for an unknown/empty type — guessing a wrong
// container port for an image ensemble doesn't recognize would be worse
// than today's host==container behavior.
func resolveContainerPort(db config.Database) int {
	if db.ContainerPort != 0 {
		return db.ContainerPort
	}
	if p, ok := defaultContainerPorts[db.Type]; ok {
		return p
	}
	return db.Port
}

// dockerRunDatabaseArgs builds dockerRunDatabase's argv as a pure function
// of name and db, so it can be unit-tested without invoking docker (task
// 3.6, brief step 1).
func dockerRunDatabaseArgs(name string, db config.Database) []string {
	args := []string{"run", "-d", "--name", dockerContainerName(name)}
	if db.Port != 0 {
		// Bind to loopback explicitly (127.0.0.1:host:container), not
		// docker's default 0.0.0.0 publish. Without this, a database
		// container whose server never comes up (or a port that shadows a
		// developer's own local server on the same port — task 3.6 defect
		// D1) is reachable from anywhere docker's published-port proxy is
		// reachable from, silently, instead of failing loudly with a bind
		// conflict against a loopback-only listener.
		args = append(args, "-p", fmt.Sprintf("127.0.0.1:%d:%d", db.Port, resolveContainerPort(db)))
	}
	for k, v := range db.Env {
		args = append(args, "-e", fmt.Sprintf("%s=%s", k, v))
	}
	args = append(args, db.Image)
	return args
}

// dockerRunDatabase starts a Database via the same driver.
func dockerRunDatabase(name string, db config.Database) error {
	return runDocker(name, dockerRunDatabaseArgs(name, db))
}

func runDocker(name string, args []string) error {
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// dockerRemove force-removes name's container. Down uses this, so an
// already-gone container is not an error.
func dockerRemove(name string) error {
	out, err := exec.Command("docker", "rm", "-f", dockerContainerName(name)).CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker rm -f %s: %w: %s", dockerContainerName(name), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// dockerWaitDelay is how long a ctx-cancelled docker command gets to die
// before its output pipes are closed out from under it. Long enough that a
// command finishing normally right at the deadline is never truncated, short
// enough that `ensemble up` still gives up promptly.
const dockerWaitDelay = 2 * time.Second

// dockerOutput runs a docker command bounded by ctx and returns its combined
// output.
//
// The WaitDelay is what makes the bound real, and it is not optional:
// CommandContext kills the process when ctx expires, but CombinedOutput does
// not return until every writer to the output pipe is gone. A killed process
// that left a child holding that pipe blocks the read forever — so the call
// outlives its own deadline and the context bounds nothing. WaitDelay closes
// the pipes shortly after the kill, which is what turns "bounded by ctx" from
// a claim into a guarantee. Proven by TestDockerContainerStateIsBoundedByContext,
// which fails without it.
func dockerOutput(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.WaitDelay = dockerWaitDelay
	return cmd.CombinedOutput()
}

// dockerContainerState reports whether name's container exists at all, and if
// so whether it is running. It is the input to startDatabase's three-way
// choice: create, adopt, or restart.
//
// "Doesn't exist" is a normal answer, not an error — docker signals it by
// failing `inspect` with "No such object", which is indistinguishable from a
// real failure by exit status alone, so the message is what separates them.
// Anything else (daemon unreachable, permission denied) is returned as an
// error rather than quietly reported as absent: treating a broken daemon as
// "no container here" would send the caller into `docker run` and turn a
// clear infrastructure problem into a confusing create failure.
//
// The command is bound to ctx. A wedged daemon does not fail `inspect`, it
// blocks — and this runs inside `ensemble up`, the command a developer runs
// most, so an unbounded call here would hang the tool outright instead of
// reporting anything. (Same defect the docker-gated tests' own skip-guard
// had; see requireDockerIntegration.)
func dockerContainerState(ctx context.Context, name string) (exists, running bool, err error) {
	cn := dockerContainerName(name)
	out, err := dockerOutput(ctx, "inspect", "-f", "{{.State.Running}}", cn)
	if err != nil {
		if isNoSuchContainer(string(out)) {
			return false, false, nil
		}
		return false, false, fmt.Errorf("docker inspect %s: %w: %s", cn, err, strings.TrimSpace(string(out)))
	}
	return true, strings.TrimSpace(string(out)) == "true", nil
}

// dockerContainerExit is dockerContainerState plus the container's exit
// code — one inspect for all three, so the supervision poll (see
// supervise.go) can classify a stopped container as exited vs crashed
// without a second round trip. ExitCode is only meaningful when exists is
// true and running is false; docker reports 0 for a running container.
func dockerContainerExit(ctx context.Context, name string) (exists, running bool, exitCode int, err error) {
	cn := dockerContainerName(name)
	out, err := dockerOutput(ctx, "inspect", "-f", "{{.State.Running}} {{.State.ExitCode}}", cn)
	if err != nil {
		if isNoSuchContainer(string(out)) {
			return false, false, 0, nil
		}
		return false, false, 0, fmt.Errorf("docker inspect %s: %w: %s", cn, err, strings.TrimSpace(string(out)))
	}
	fields := strings.Fields(strings.TrimSpace(string(out)))
	if len(fields) != 2 {
		return false, false, 0, fmt.Errorf("docker inspect %s: unexpected output %q", cn, strings.TrimSpace(string(out)))
	}
	code, convErr := strconv.Atoi(fields[1])
	if convErr != nil {
		return false, false, 0, fmt.Errorf("docker inspect %s: exit code %q: %w", cn, fields[1], convErr)
	}
	return true, fields[0] == "true", code, nil
}

// isNoSuchContainer matches docker's "container is not there" wording. Both
// spellings are checked because the message has differed across docker
// versions and podman prints its own; matching case-insensitively on the
// stable part keeps a version bump from turning "absent" into a hard error.
func isNoSuchContainer(out string) bool {
	l := strings.ToLower(out)
	return strings.Contains(l, "no such object") || strings.Contains(l, "no such container")
}

// dockerStart restarts an existing, stopped container in place, keeping the
// volumes and data it already has — which is the entire reason to prefer this
// over remove-and-recreate for a database.
func dockerStart(ctx context.Context, name string) error {
	cn := dockerContainerName(name)
	out, err := dockerOutput(ctx, "start", cn)
	if err != nil {
		return fmt.Errorf("docker start %s: %w: %s", cn, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// DatabaseContainerRunning reports whether the container ensemble manages for
// database name exists and is currently running — that is, whether Up would
// adopt it rather than create one.
//
// It is exported for the CLI's preflight port check, which runs before the
// orchestrator and would otherwise refuse to start on the very port the
// adopted container is holding. A daemon that cannot answer is not an
// adoptable container, so callers treat an error as "no" and fall back to
// their normal conflict handling; the error is returned for diagnostics only.
func DatabaseContainerRunning(ctx context.Context, name string) (bool, error) {
	exists, running, err := dockerContainerState(ctx, name)
	if err != nil {
		return false, err
	}
	return exists && running, nil
}

// dockerRunning reports whether name's container is currently running.
func dockerRunning(name string) (bool, error) {
	out, err := exec.Command("docker", "inspect", "-f", "{{.State.Running}}", dockerContainerName(name)).Output()
	if err != nil {
		return false, fmt.Errorf("docker inspect %s: %w", dockerContainerName(name), err)
	}
	return strings.TrimSpace(string(out)) == "true", nil
}

// pollDockerRunning polls dockerRunning until it reports true or timeout
// elapses — the docker half of the no-health-path gate ("container is
// running").
func pollDockerRunning(ctx context.Context, name string, timeout time.Duration) error {
	err := pollUntil(ctx, timeout, func() (bool, error) {
		return dockerRunning(name)
	})
	if err != nil {
		return fmt.Errorf("docker container %s: %w", dockerContainerName(name), err)
	}
	return nil
}
