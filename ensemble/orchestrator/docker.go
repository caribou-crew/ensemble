package orchestrator

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/ensemble-dev/ensemble/ensemble/config"
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
	args := []string{"run", "-d", "--name", dockerContainerName(name)}
	for _, p := range d.Ports {
		args = append(args, "-p", p)
	}
	for k, v := range d.Env {
		args = append(args, "-e", fmt.Sprintf("%s=%s", k, v))
	}
	args = append(args, d.Image)
	return runDocker(name, args)
}

// dockerRunDatabase starts a Database via the same driver.
func dockerRunDatabase(name string, db config.Database) error {
	args := []string{"run", "-d", "--name", dockerContainerName(name)}
	if db.Port != 0 {
		args = append(args, "-p", fmt.Sprintf("%d:%d", db.Port, db.Port))
	}
	for k, v := range db.Env {
		args = append(args, "-e", fmt.Sprintf("%s=%s", k, v))
	}
	args = append(args, db.Image)
	return runDocker(name, args)
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
