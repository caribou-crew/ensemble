package orchestrator

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
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
