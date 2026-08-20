package orchestrator

import (
	"context"
	"fmt"
	"syscall"
)

// Flip switches name between native and container placement at runtime:
// it stops the current placement (process kill / docker rm) and starts
// the other, without touching the proxy listener — wireProxy runs once,
// at Up, and its Listen/Upstream addresses (127.0.0.1:ProxyPort ->
// 127.0.0.1:Port) are unchanged by which placement backs Port. A docker
// placement must therefore publish its container port on the same host
// Port the service declares, so the upstream keeps resolving after the
// flip.
//
// Flip requires the service to declare both `run` and `docker`; a service
// with only one placement errors rather than silently no-op'ing.
func (o *Orchestrator) Flip(ctx context.Context, name string) error {
	active := o.cfg.ServicesForProfiles(o.opts.Profiles)
	svc, ok := active[name]
	if !ok {
		return fmt.Errorf("orchestrator: flip %q: not an active service", name)
	}
	if svc.Run == "" || svc.Docker == nil {
		return fmt.Errorf("orchestrator: flip %q: service %s has no alternate placement", name, name)
	}

	// Serialize against any concurrent Flip/Restart/Down teardown on this
	// same service — see the serviceLocks field comment on Orchestrator.
	// Held across the whole read-act-mutate span below, not just the map
	// accesses.
	unlock := o.lockService(name)
	defer unlock()

	o.mu.Lock()
	cmd, hasProc := o.procs[name]
	isDocker := o.dockerNodes[name]
	o.mu.Unlock()

	var target string
	switch {
	case isDocker:
		target = "native"
		if err := o.removeDockerContainer(name); err != nil {
			wrapped := fmt.Errorf("stop previous container: %w", err)
			o.fail(name, wrapped)
			return fmt.Errorf("orchestrator: flip %q: %w", name, wrapped)
		}
		o.mu.Lock()
		delete(o.dockerNodes, name)
		o.mu.Unlock()
	case hasProc:
		target = "docker"
		if cmd.Process != nil {
			if err := o.killGroup(cmd.Process.Pid, syscall.SIGKILL); err != nil {
				wrapped := fmt.Errorf("stop previous process (pid %d): %w", cmd.Process.Pid, err)
				o.fail(name, wrapped)
				return fmt.Errorf("orchestrator: flip %q: %w", name, wrapped)
			}
		}
		o.mu.Lock()
		delete(o.procs, name)
		o.mu.Unlock()
	default:
		return fmt.Errorf("orchestrator: flip %q: service %s is not running", name, name)
	}

	return o.startServiceAs(ctx, name, svc, target)
}
