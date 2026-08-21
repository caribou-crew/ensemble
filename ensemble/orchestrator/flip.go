package orchestrator

import (
	"context"
	"fmt"
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
	if _, ok := o.activeServices()[name]; !ok {
		return fmt.Errorf("orchestrator: flip %q: not an active service", name)
	}
	svc, err := o.resolve(name)
	if err != nil {
		return fmt.Errorf("orchestrator: flip %q: %w", name, err)
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

	hadProc, wasDocker, err := o.stopCurrent(name)
	if err != nil {
		o.fail(name, err)
		return fmt.Errorf("orchestrator: flip %q: %w", name, err)
	}
	var target string
	switch {
	case wasDocker:
		target = "native"
	case hadProc:
		target = "docker"
	default:
		return fmt.Errorf("orchestrator: flip %q: service %s is not running", name, name)
	}

	return o.startServiceAs(ctx, name, svc, target)
}
