package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"

	"github.com/caribou-crew/ensemble/core/proxy"
	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/ensemble/config"
)

// ReconcileAction is one unit's outcome from a Reconcile call.
type ReconcileAction struct {
	Kind   string `json:"kind"`   // "service" | "database" | "gateway" | "stub" | "entity" | "global"
	Name   string `json:"name"`
	Action string `json:"action"` // "started" | "restarted" | "stopped" | "rebound" | "updated" | "unchanged"
}

// ReconcileResult reports what Reconcile did, per unit — nothing about a
// unit that didn't change is omitted; "unchanged" is a real, reportable
// outcome so a caller can show the whole picture, not just what moved.
type ReconcileResult struct {
	Actions []ReconcileAction `json:"actions"`
}

func (r *ReconcileResult) add(kind, name, action string) {
	r.Actions = append(r.Actions, ReconcileAction{Kind: kind, Name: name, Action: action})
}

// Reconcile diffs newCfg against the config most recently applied (by Up or
// a prior Reconcile) and touches only what changed: a service/database/stub
// whose block differs is restarted, one added is started, one removed is
// stopped; a changed gateway is rebound (closed and re-listened) without
// touching anything else — the port-change-without-a-full-restart case this
// exists for. Entities and stack-wide settings (redact, trace/source
// headers, latency defaults) have no process to restart, so a change there
// is applied in place. An unrelated unit — one whose config block is
// byte-for-byte the same — is never touched, and a service/stub outside the
// currently active profile set is only having its config recorded, not
// started (matching Up's own active-set gating).
//
// Reconcile never cascades to a changed unit's dependents (DependsOn) — a
// deliberate simplicity choice: only what's literally different in newCfg
// is touched.
func (o *Orchestrator) Reconcile(ctx context.Context, newCfg config.Config) (*ReconcileResult, error) {
	o.mu.Lock()
	old := o.lastConfig
	o.mu.Unlock()

	result := &ReconcileResult{}
	var errs []error

	if err := o.reconcileGateways(old, newCfg, result); err != nil {
		errs = append(errs, err)
	}
	if err := o.reconcileStubs(old, newCfg, result); err != nil {
		errs = append(errs, err)
	}
	if err := o.reconcileDatabases(ctx, old, newCfg, result); err != nil {
		errs = append(errs, err)
	}
	if err := o.reconcileServices(ctx, old, newCfg, result); err != nil {
		errs = append(errs, err)
	}
	o.reconcileEntities(old, newCfg, result)
	o.reconcileGlobals(old, newCfg, result)

	o.mu.Lock()
	*o.cfg = newCfg
	o.lastConfig = newCfg
	o.mu.Unlock()

	o.recomputeWiringWarnings()

	return result, errors.Join(errs...)
}

// changedNames returns, sorted, every key present in oldM or newM whose
// value differs (added, removed, or changed) between the two maps, via
// reflect.DeepEqual — a whole-block comparison, not a field-aware diff:
// any difference at all is treated as a change. added/removed report which
// side is missing the key.
func changedNames[V any](oldM, newM map[string]V) (added, removed, changed, unchanged []string) {
	for name, nv := range newM {
		ov, existed := oldM[name]
		switch {
		case !existed:
			added = append(added, name)
		case !reflect.DeepEqual(ov, nv):
			changed = append(changed, name)
		default:
			unchanged = append(unchanged, name)
		}
	}
	for name := range oldM {
		if _, stillThere := newM[name]; !stillThere {
			removed = append(removed, name)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	sort.Strings(changed)
	sort.Strings(unchanged)
	return
}

// --- gateways ---

func (o *Orchestrator) reconcileGateways(old, newCfg config.Config, result *ReconcileResult) error {
	added, removed, changed, unchanged := changedNames(old.Gateways, newCfg.Gateways)

	for _, name := range removed {
		o.unwireGateway(name)
		result.add("gateway", name, "stopped")
	}
	for _, name := range unchanged {
		result.add("gateway", name, "unchanged")
	}

	var errs []error
	for _, name := range changed {
		o.unwireGateway(name)
		if err := o.wireOneGateway(name, newCfg.Gateways[name], "local"); err != nil {
			errs = append(errs, err)
			continue
		}
		result.add("gateway", name, "rebound")
	}
	for _, name := range added {
		if err := o.wireOneGateway(name, newCfg.Gateways[name], "local"); err != nil {
			errs = append(errs, err)
			continue
		}
		result.add("gateway", name, "started")
	}
	return errors.Join(errs...)
}

// --- stubs ---

func (o *Orchestrator) reconcileStubs(old, newCfg config.Config, result *ReconcileResult) error {
	added, removed, changed, unchanged := changedNames(old.Stubs, newCfg.Stubs)

	for _, name := range removed {
		o.stopStub(name)
		result.add("stub", name, "stopped")
	}
	for _, name := range unchanged {
		result.add("stub", name, "unchanged")
	}

	var errs []error
	for _, name := range changed {
		o.stopStub(name)
		if err := o.startStub(name, newCfg.Stubs[name]); err != nil {
			errs = append(errs, err)
			continue
		}
		result.add("stub", name, "restarted")
	}
	for _, name := range added {
		if err := o.startStub(name, newCfg.Stubs[name]); err != nil {
			errs = append(errs, err)
			continue
		}
		result.add("stub", name, "started")
	}
	return errors.Join(errs...)
}

// --- databases ---

func (o *Orchestrator) reconcileDatabases(ctx context.Context, old, newCfg config.Config, result *ReconcileResult) error {
	added, removed, changed, unchanged := changedNames(old.Databases, newCfg.Databases)

	var errs []error
	for _, name := range removed {
		if o.running(name) {
			if _, _, err := o.stopCurrent(name); err != nil {
				errs = append(errs, fmt.Errorf("database %s: %w", name, err))
				continue
			}
			o.setState(name, func(s *ServiceState) { s.Status = StatusStopped; s.LastErr = "" })
		}
		result.add("database", name, "stopped")
	}
	for _, name := range unchanged {
		result.add("database", name, "unchanged")
	}
	for _, name := range changed {
		if o.running(name) {
			if _, _, err := o.stopCurrent(name); err != nil {
				errs = append(errs, fmt.Errorf("database %s: %w", name, err))
				continue
			}
		}
		if err := o.startDatabase(ctx, name, newCfg.Databases[name]); err != nil {
			errs = append(errs, err)
			continue
		}
		result.add("database", name, "restarted")
	}
	for _, name := range added {
		if err := o.startDatabase(ctx, name, newCfg.Databases[name]); err != nil {
			errs = append(errs, err)
			continue
		}
		result.add("database", name, "started")
	}
	return errors.Join(errs...)
}

// --- services ---

// reconcileServices applies add/remove/change actions to the services whose
// config block differs, but only touches one that participates under the
// CURRENTLY active profile set (newCfg.ServicesForProfiles(o.activeProfiles())
// — Reconcile never itself activates/deactivates a profile, that's
// UpProfiles/DownProfiles's job) — mirroring Up's own active-set gating so
// an inactive lane's service is recorded in cfg but never started. A
// dependency-ordered walk (topoOrder over the new active set) keeps a newly
// added service starting after whatever it depends on, the same guarantee
// Up itself provides.
func (o *Orchestrator) reconcileServices(ctx context.Context, old, newCfg config.Config, result *ReconcileResult) error {
	added, removed, changed, unchanged := changedNames(old.Services, newCfg.Services)
	touch := map[string]string{} // name -> "added" | "changed"
	for _, n := range added {
		touch[n] = "added"
	}
	for _, n := range changed {
		touch[n] = "changed"
	}

	var errs []error

	// Removed services are stopped unconditionally (they can't be "active"
	// in newCfg — they no longer exist there), regardless of order.
	for _, name := range removed {
		if o.running(name) {
			unlock := o.lockService(name)
			_, _, err := o.stopCurrent(name)
			unlock()
			if err != nil {
				errs = append(errs, fmt.Errorf("service %s: %w", name, err))
				continue
			}
			o.setState(name, func(s *ServiceState) { s.Status = StatusStopped; s.PID = 0; s.LastErr = "" })
		}
		result.add("service", name, "stopped")
	}
	for _, name := range unchanged {
		result.add("service", name, "unchanged")
	}

	nc := newCfg
	activeNew := nc.ServicesForProfiles(o.activeProfiles())
	order, err := topoOrderOver(activeNew, newCfg.Databases)
	if err != nil {
		errs = append(errs, err)
		return errors.Join(errs...)
	}
	for _, name := range order {
		kind, ok := touch[name]
		if !ok {
			continue
		}
		if _, active := activeNew[name]; !active {
			// Recorded in cfg (the final *o.cfg = newCfg swap below covers
			// that) but not started — same as an inactive service Up
			// leaves alone.
			continue
		}
		svc := activeNew[name]
		if kind == "changed" && o.running(name) {
			unlock := o.lockService(name)
			_, _, err := o.stopCurrent(name)
			unlock()
			if err != nil {
				errs = append(errs, fmt.Errorf("service %s: %w", name, err))
				continue
			}
		}
		if err := o.startService(ctx, name, svc); err != nil {
			errs = append(errs, err)
			continue
		}
		if err := o.wireProxy(name, svc); err != nil {
			errs = append(errs, err)
			continue
		}
		if kind == "changed" {
			result.add("service", name, "restarted")
		} else {
			result.add("service", name, "started")
		}
	}
	return errors.Join(errs...)
}

// --- entities ---

// reconcileEntities has no process to restart — cfg.Entities is served
// directly off the shared *config.Config every request (see
// ensemble/server's entity routes), so the final *o.cfg = newCfg swap in
// Reconcile is the whole mechanism. This only computes what to report.
func (o *Orchestrator) reconcileEntities(old, newCfg config.Config, result *ReconcileResult) {
	added, removed, changed, unchanged := changedNames(old.Entities, newCfg.Entities)
	for _, name := range removed {
		result.add("entity", name, "stopped")
	}
	for _, name := range changed {
		result.add("entity", name, "updated")
	}
	for _, name := range added {
		result.add("entity", name, "started")
	}
	for _, name := range unchanged {
		result.add("entity", name, "unchanged")
	}
}

// --- global, stack-wide settings ---

// ApplyProxyGlobals copies every stack-wide proxy setting out of a config
// onto a live proxy. It exists so `ensemble up`'s startup wiring and
// reconcileGlobals' hot-reload path cannot disagree about WHICH settings
// those are: the two are the same list, and a setting added to one and
// forgotten in the other is a config key that works after a reload and not
// on a cold start (or the reverse) — indistinguishable from a typo, from the
// user's side.
//
// It is unconditional: every field is assigned, whether or not it changed.
// reconcileGlobals below deliberately does NOT call it, because it must
// report per-key actions and therefore has to compare first — but it is
// checked against this function by TestReconcileGlobalsCoversEveryProxyGlobal.
//
// OnWarn is not here: its sink is the caller's business (a CLI has stderr, a
// test has a slice, an embedder may have neither) and nothing in the config
// names it.
func ApplyProxyGlobals(px *proxy.Proxy, cfg config.Config) {
	px.TraceHeader = cfg.TraceHeader
	px.SourceHeaders = cfg.SourceHeaders
	px.ClientHeaders = cfg.ClientIdentityHeaders
}

// reconcileGlobals applies a change to any stack-wide setting that a live
// runtime object holds onto rather than re-reading from cfg per request:
// the proxy's TraceHeader/SourceHeaders, the Recorder's redaction rules,
// and the LatencyStore's configured defaults. None of these ever restart a
// service or gateway — see the Reconcile doc comment.
func (o *Orchestrator) reconcileGlobals(old, newCfg config.Config, result *ReconcileResult) {
	if old.TraceHeader != newCfg.TraceHeader {
		o.px.TraceHeader = newCfg.TraceHeader
		result.add("global", "trace_header", "updated")
	}
	if !reflect.DeepEqual(old.SourceHeaders, newCfg.SourceHeaders) {
		o.px.SourceHeaders = newCfg.SourceHeaders
		result.add("global", "source_header", "updated")
	}
	if !reflect.DeepEqual(old.ClientIdentityHeaders, newCfg.ClientIdentityHeaders) {
		o.px.ClientHeaders = newCfg.ClientIdentityHeaders
		result.add("global", "client_identity_headers", "updated")
	}
	if !reflect.DeepEqual(old.Redact, newCfg.Redact) && o.Rec != nil {
		redactor, err := trace.NewRedactor(trace.DestroyKeys(newCfg.Redact), 0, nil)
		if err != nil {
			// Unreachable: DestroyKeys only ever produces ModeDestroy
			// rules, which never need a data key — see
			// core/trace.NewRedactor.
			panic(err)
		}
		o.Rec.SetRedactor(redactor)
		result.add("global", "redact", "updated")
	}
	if !reflect.DeepEqual(old.Latency, newCfg.Latency) && o.px.Latency != nil {
		o.px.Latency.Reset()
		for _, d := range newCfg.Latency.Defaults {
			o.px.Latency.Set(latencyRuleFrom(d))
		}
		result.add("global", "latency", "updated")
	}
}

// latencyRuleFrom maps a config.LatencyDefault onto the proxy.LatencyRule
// shape LatencyStore.Set expects — the same mapping cmd_up.go's runUp uses
// to seed it at startup.
func latencyRuleFrom(d config.LatencyDefault) proxy.LatencyRule {
	return proxy.LatencyRule{
		Target: d.Target, Path: d.Path,
		FixedMs: d.FixedMs, P50: d.P50, P95: d.P95, P99: d.P99,
		Enabled: d.Enabled,
	}
}
