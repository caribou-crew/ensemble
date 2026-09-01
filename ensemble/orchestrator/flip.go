package orchestrator

import (
	"context"
	"fmt"

	"github.com/caribou-crew/ensemble/ensemble/config"
)

// Flip switches name between native and container placement at runtime —
// the two-placement case FlipTo generalizes. Requires the service to
// declare both `run` and `docker`; a service with only one local placement
// errors rather than silently no-op'ing. Kept as its own entry point
// (rather than asking every caller to compute "the other one") since a
// binary toggle is what the CLI/TUI/dashboard have always called.
func (o *Orchestrator) Flip(ctx context.Context, name string) error {
	svc, err := o.resolve(name)
	if err != nil {
		return fmt.Errorf("orchestrator: flip %q: %w", name, err)
	}
	if svc.Run == "" || svc.Docker == nil {
		return fmt.Errorf("orchestrator: flip %q: service %s has no alternate placement", name, name)
	}
	current, ok := o.Service(name)
	if !ok {
		return fmt.Errorf("orchestrator: flip %q: not an active service", name)
	}
	target := "docker"
	if current.Placement == "docker" {
		target = "native"
	}
	return o.flipTo(ctx, name, svc, target)
}

// FlipTo switches name to an explicit placement — "native", "docker", or
// "passthrough" — rather than inferring "the other one": three placements
// need naming, not toggling. Errors if the service doesn't declare the
// requested placement at all, same "no silent no-op" rule Flip already
// enforces for native/docker.
func (o *Orchestrator) FlipTo(ctx context.Context, name, target string) error {
	svc, err := o.resolve(name)
	if err != nil {
		return fmt.Errorf("orchestrator: flip %q: %w", name, err)
	}
	switch target {
	case "native":
		if svc.Run == "" {
			return fmt.Errorf("orchestrator: flip %q: service %s has no native placement configured", name, name)
		}
	case "docker":
		if svc.Docker == nil {
			return fmt.Errorf("orchestrator: flip %q: service %s has no docker placement configured", name, name)
		}
	case "passthrough":
		if svc.Upstream == "" {
			return fmt.Errorf("orchestrator: flip %q: service %s has no passthrough placement configured (upstream is empty)", name, name)
		}
	default:
		return fmt.Errorf("orchestrator: flip %q: unknown placement %q", name, target)
	}
	return o.flipTo(ctx, name, svc, target)
}

// flipTo is the shared core: stop whatever is currently running (a no-op
// when the service is already in a process-less placement, e.g.
// passthrough), start the requested placement, and re-wire the proxy
// listener — a no-op for native<->docker (same resolved 127.0.0.1:port,
// see resolveProxyUpstream) but load-bearing for passthrough, whose
// resolved upstream is a different address entirely.
func (o *Orchestrator) flipTo(ctx context.Context, name string, svc config.Service, target string) error {
	if _, ok := o.activeServices()[name]; !ok {
		return fmt.Errorf("orchestrator: flip %q: not an active service", name)
	}

	// Serialize against any concurrent Flip/FlipTo/Restart/Down teardown on
	// this same service — see the serviceLocks field comment on
	// Orchestrator. Held across the whole read-act-mutate span below, not
	// just the map accesses.
	unlock := o.lockService(name)
	defer unlock()

	if _, _, err := o.stopCurrent(name); err != nil {
		o.fail(name, err)
		return fmt.Errorf("orchestrator: flip %q: %w", name, err)
	}
	if err := o.startServiceAs(ctx, name, svc, target); err != nil {
		return err
	}
	if err := o.wireProxy(name, svc); err != nil {
		return err
	}
	// Flip doesn't change the variant selection, so this rarely flips a
	// warning's answer — but it's cheap, and the design brief calls it out
	// as a re-evaluation hook alongside SetVariant, so it stays exact
	// rather than relying on the caller triggering a coincidental Restart.
	o.recomputeWiringWarnings()
	return nil
}
