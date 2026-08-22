// Package orchestrator supervises the services and databases in an
// ensemble.yaml stack: it starts them in dependency order, gates each on
// health, rebuilds stale services, and wires the intercepting proxy in
// front of every service that wants one. Native services run as directly
// supervised OS processes (their own process group, so Down reaps shell
// children too); docker-placed services and every database run as
// containers via the `docker` CLI (no SDK dependency).
package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/caribou-crew/ensemble/core/proxy"
	"github.com/caribou-crew/ensemble/ensemble/config"
)

// Status is a ServiceState's lifecycle stage.
type Status string

const (
	StatusStopped   Status = "stopped"
	StatusBuilding  Status = "building"
	StatusStarting  Status = "starting"
	StatusHealthy   Status = "healthy"
	StatusUnhealthy Status = "unhealthy"
	StatusFailed    Status = "failed"
)

// ServiceState is a snapshot of one supervised node (service or database).
type ServiceState struct {
	Name      string    `json:"name"`
	Status    Status    `json:"status"`
	Placement string    `json:"placement"`     // "native" | "docker"
	PID       int       `json:"pid,omitempty"` // native only; 0 for docker
	ProxyPort int       `json:"proxyPort,omitempty"`
	Port      int       `json:"port,omitempty"`
	Variant   string    `json:"variant,omitempty"` // current config.Service variant, if it declares any
	StartedAt time.Time `json:"startedAt,omitzero"`
	LastErr   string    `json:"lastErr,omitempty"`
	// RSSKB is resident memory in KB, sampled best-effort at read time (see
	// WithMemory) — 0 when unsampled or the node isn't currently running.
	// Not tracked continuously: States()/Service() never set it, only
	// WithMemory's own returned copy does.
	RSSKB int64 `json:"rssKB,omitempty"`
}

// Opts configures an Orchestrator.
type Opts struct {
	// Profiles is the set of active profile names, passed to
	// config.Config.ServicesForProfiles to pick the active service set.
	Profiles []string
	// Logf receives orchestrator-level progress messages (not service
	// stdout/stderr, which goes to LogDir). Nil disables logging.
	Logf func(string, ...any)
	// LogDir holds per-service log files (<name>.log) and build stamps
	// (<name>.buildstamp). Defaults to "<cfg.Dir>/.ensemble/run".
	LogDir string
	// HealthTimeout bounds how long a health/TCP/docker gate waits before
	// failing the service. Defaults to 30s.
	HealthTimeout time.Duration
	// Variants overrides config.Service.Default per service for this run
	// (`ensemble up --variant svc=real`). cmd_up validates the names
	// against the config before building the Orchestrator.
	Variants map[string]string
}

// Orchestrator supervises the services and databases of one config.Config.
type Orchestrator struct {
	cfg  *config.Config
	px   *proxy.Proxy
	opts Opts

	mu          sync.Mutex
	states      map[string]*ServiceState
	procs       map[string]*exec.Cmd // native nodes with a running process
	dockerNodes map[string]bool      // nodes currently running as containers
	// variants is each service's chosen config.Service variant, when it
	// declares any: seeded from Opts.Variants, else the config default,
	// and changed by SetVariant. Read through currentVariant.
	variants map[string]string
	// active is the live profile set, seeded from Opts.Profiles and changed
	// by UpProfiles/DownProfiles; every "which services exist right now"
	// question goes through activeServices.
	active map[string]bool
	// wired records services whose intercept listener is bound, so
	// UpProfiles re-adding a lane never tries to rebind it.
	wired map[string]bool

	// serviceLocks holds one mutex per service name, serializing Flip,
	// Restart, and Down's per-service teardown against each other for a
	// given name — guarding the whole read-current-placement, act (kill /
	// docker rm / start replacement), mutate-maps span. Without this, two
	// concurrent operations on the SAME service (e.g. Flip racing Restart)
	// can both read the pre-op placement before either commits its
	// teardown, then both start a replacement, leaving the maps with both
	// placements tracked (a live orphan neither op knows about) or one
	// clobbering the other's entry (an untracked, still-live process or
	// container that Down will never find). Different services use
	// different mutexes, so operations on different services still run
	// fully concurrently — only mu (below) is used to guard the map itself,
	// and only briefly, never held across a kill/start/health-wait.
	serviceLocks map[string]*sync.Mutex

	// testStartHook, set only from tests, is called with each node's name
	// the moment Up begins starting it — proof of dependency order without
	// depending on process-timing.
	testStartHook func(name string)

	// killGroup and removeDockerContainer are indirections over the
	// package-level killProcessGroup/dockerRemove, so tests can inject a
	// failure and exercise Restart's abort-on-error path without needing
	// a real, hard-to-provoke OS failure (e.g. EPERM). Default to the real
	// implementations in New.
	killGroup             func(pid int, sig syscall.Signal) error
	removeDockerContainer func(name string) error

	// SQLRunner executes seed SQL files against a database; Task 2.5 owns
	// the concrete drivers. Nil until the caller sets it, in which case
	// Seed's SQL steps fail cleanly rather than panicking.
	SQLRunner SQLRunner

	// DBReady performs a genuine protocol-level readiness check against a
	// managed database (e.g. issuing a real query through its driver),
	// used by startDatabase's health gate in place of a bare TCP dial
	// (task 3.6, defect D3: a bare TCP dial succeeds against docker's
	// published-port proxy even when nothing — or the wrong server — is
	// listening behind it, so it can never observe a database that's
	// actually unreachable or misconfigured). cmd_up.go wires this from
	// the same ensemble/inspector drivers it builds for the dashboard, so
	// the readiness check and the dashboard's reads go through identical
	// connection logic.
	//
	// Nil falls back to the legacy bare TCP dial — used by tests (and any
	// database type ensemble/inspector has no driver for) that don't wire
	// an inspector. Not importing ensemble/inspector directly from this
	// package is deliberate: it's the caller's dependency to own (see
	// cmd_up.go's buildInspector), not the orchestrator's.
	DBReady DBReadyFunc
}

// DBReadyFunc reports whether name's database is ready to serve real
// queries — see the DBReady field comment. A nil error means ready; any
// error (including ctx expiring) means not yet, and the caller should
// retry until Orchestrator.Opts.HealthTimeout elapses.
type DBReadyFunc func(ctx context.Context, name string, db config.Database) error

// New builds an Orchestrator over cfg, wiring active services' proxy
// targets into px during Up.
func New(cfg *config.Config, px *proxy.Proxy, opts Opts) *Orchestrator {
	if opts.HealthTimeout <= 0 {
		opts.HealthTimeout = 30 * time.Second
	}
	if opts.LogDir == "" {
		opts.LogDir = filepath.Join(cfg.Dir, ".ensemble", "run")
	}
	return &Orchestrator{
		cfg:                   cfg,
		px:                    px,
		opts:                  opts,
		states:                map[string]*ServiceState{},
		procs:                 map[string]*exec.Cmd{},
		dockerNodes:           map[string]bool{},
		variants:              variantsFrom(cfg, opts.Variants),
		active:                profileSet(opts.Profiles),
		wired:                 map[string]bool{},
		serviceLocks:          map[string]*sync.Mutex{},
		killGroup:             killProcessGroup,
		removeDockerContainer: dockerRemove,
	}
}

func profileSet(names []string) map[string]bool {
	out := make(map[string]bool, len(names))
	for _, n := range names {
		out[n] = true
	}
	return out
}

// activeProfiles is the live profile set, sorted.
func (o *Orchestrator) activeProfiles() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]string, 0, len(o.active))
	for n := range o.active {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// activeServices is the service set the live profiles select — see
// config.Config.ServicesForProfiles for the any-active-profile rule that
// makes a service shared by two lanes stay up while either is active.
func (o *Orchestrator) activeServices() map[string]config.Service {
	return o.cfg.ServicesForProfiles(o.activeProfiles())
}

// ProfileInfo is one configured profile and its live state.
type ProfileInfo struct {
	Name     string   `json:"name"`
	Services []string `json:"services"`
	Active   bool     `json:"active"`
}

// ProfilesState is the full profile picture: every profile the config
// mentions, with members and whether it's active right now.
type ProfilesState struct {
	Active   []string      `json:"active"`
	Profiles []ProfileInfo `json:"profiles"`
}

// Profiles reports every configured profile with its live state.
func (o *Orchestrator) Profiles() ProfilesState {
	active := o.activeProfiles()
	activeSet := profileSet(active)
	st := ProfilesState{Active: active, Profiles: []ProfileInfo{}}
	for _, name := range o.cfg.ProfileNames() {
		st.Profiles = append(st.Profiles, ProfileInfo{Name: name, Services: o.cfg.ProfileMembers(name), Active: activeSet[name]})
	}
	return st
}

func (o *Orchestrator) checkProfiles(names []string) error {
	known := profileSet(o.cfg.ProfileNames())
	for _, n := range names {
		if !known[n] {
			return fmt.Errorf("orchestrator: unknown profile %q (have %s)", n, strings.Join(o.cfg.ProfileNames(), ", "))
		}
	}
	return nil
}

// running reports whether name has a tracked process or container.
func (o *Orchestrator) running(name string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	_, hasProc := o.procs[name]
	return hasProc || o.dockerNodes[name]
}

// UpProfiles activates names and starts — in dependency order, exactly as
// Up does — every service or database the enlarged set covers that isn't
// already running. Services already up (shared between lanes, or
// always-on) are untouched. The first failure stops the walk and is
// returned; the profiles stay active so a Restart can recover the
// offender.
func (o *Orchestrator) UpProfiles(ctx context.Context, names []string) error {
	if err := o.checkProfiles(names); err != nil {
		return err
	}
	o.mu.Lock()
	for _, n := range names {
		o.active[n] = true
	}
	o.mu.Unlock()

	active := o.activeServices()
	order, err := o.topoOrder(active)
	if err != nil {
		return err
	}
	for _, name := range order {
		if o.running(name) {
			continue
		}
		if _, ok := active[name]; ok {
			svc, err := o.resolve(name)
			if err != nil {
				o.fail(name, err)
				return fmt.Errorf("orchestrator: %s: %w", name, err)
			}
			o.logf("orchestrator: starting service %s", name)
			if err := o.startService(ctx, name, svc); err != nil {
				return err
			}
			if err := o.wireProxy(name, svc); err != nil {
				return err
			}
			continue
		}
		if db, ok := o.cfg.Databases[name]; ok {
			o.logf("orchestrator: starting database %s", name)
			if err := o.startDatabase(ctx, name, db); err != nil {
				return err
			}
		}
	}
	return nil
}

// DownProfiles deactivates names and stops, dependents first, every
// running service the remaining active set no longer covers. A service
// another active profile still names, or one with no profile at all, keeps
// running; databases are never touched. Proxy listeners stay bound — a
// 502 hop is an honest "that lane is down". Teardown errors are collected
// and joined rather than aborting the walk.
func (o *Orchestrator) DownProfiles(names []string) error {
	if err := o.checkProfiles(names); err != nil {
		return err
	}
	o.mu.Lock()
	for _, n := range names {
		delete(o.active, n)
	}
	o.mu.Unlock()

	keep := o.activeServices()
	order, err := o.topoOrder(o.cfg.Services)
	if err != nil {
		return err
	}
	var errs []error
	for i := len(order) - 1; i >= 0; i-- {
		name := order[i]
		if _, isSvc := o.cfg.Services[name]; !isSvc {
			continue
		}
		if _, ok := keep[name]; ok || !o.running(name) {
			continue
		}
		func() {
			unlock := o.lockService(name)
			defer unlock()
			o.logf("orchestrator: stopping service %s", name)
			if _, _, err := o.stopCurrent(name); err != nil {
				o.fail(name, err)
				errs = append(errs, fmt.Errorf("%s: %w", name, err))
				return
			}
			o.setState(name, func(s *ServiceState) {
				s.Status = StatusStopped
				s.PID = 0
				s.LastErr = ""
			})
		}()
	}
	return errors.Join(errs...)
}

// variantsFrom seeds the per-service variant choice: the override when one
// is given, else the config default. Services without variants get no
// entry.
func variantsFrom(cfg *config.Config, overrides map[string]string) map[string]string {
	out := map[string]string{}
	for name, svc := range cfg.Services {
		if len(svc.Variants) == 0 {
			continue
		}
		if v, ok := overrides[name]; ok {
			out[name] = v
		} else {
			out[name] = svc.DefaultVariant()
		}
	}
	return out
}

// currentVariant is name's chosen variant, "" for a service without any.
func (o *Orchestrator) currentVariant(name string) string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.variants[name]
}

// resolve flattens name's config through its current variant — the only
// form startServiceAs & co. ever see.
func (o *Orchestrator) resolve(name string) (config.Service, error) {
	return o.cfg.ResolveService(name, o.currentVariant(name))
}

// Variant returns name's current variant and the variants it declares
// (sorted); both empty for a service without variants.
func (o *Orchestrator) Variant(name string) (current string, available []string) {
	svc, ok := o.cfg.Services[name]
	if !ok || len(svc.Variants) == 0 {
		return "", nil
	}
	return o.currentVariant(name), svc.VariantNames()
}

// SetVariant switches name to the named variant. A running service is
// stopped (whichever placement is live) and the variant started in its
// default placement, health-gated — Flip with a different target, and
// like Flip it never touches the proxy listener: the port is the
// service's, not the variant's. A service that isn't running only records
// the choice, which its next start honours. Restart and Flip resolve
// through the same choice, so neither reverts to the config default.
func (o *Orchestrator) SetVariant(ctx context.Context, name, variant string) error {
	svc, ok := o.cfg.Services[name]
	if !ok {
		return fmt.Errorf("orchestrator: variant %q: unknown service", name)
	}
	if len(svc.Variants) == 0 {
		return fmt.Errorf("orchestrator: variant %q: service %s declares no variants", name, name)
	}
	resolved, err := o.cfg.ResolveService(name, variant)
	if err != nil {
		return fmt.Errorf("orchestrator: variant %q: %w", name, err)
	}

	unlock := o.lockService(name)
	defer unlock()

	o.mu.Lock()
	_, hasProc := o.procs[name]
	isDocker := o.dockerNodes[name]
	o.mu.Unlock()

	if !hasProc && !isDocker {
		o.mu.Lock()
		o.variants[name] = variant
		o.mu.Unlock()
		o.setState(name, func(s *ServiceState) { s.Variant = variant })
		return nil
	}
	if _, _, err := o.stopCurrent(name); err != nil {
		o.fail(name, err)
		return fmt.Errorf("orchestrator: variant %q: %w", name, err)
	}
	o.mu.Lock()
	o.variants[name] = variant
	o.mu.Unlock()
	return o.startServiceAs(ctx, name, resolved, defaultPlacement(resolved))
}

// stopCurrent tears down whichever placement of name is live and forgets
// it, reporting which it was. Caller holds name's service lock. A
// teardown error is returned before the maps are touched, so a
// possibly-still-live predecessor stays tracked rather than orphaned.
func (o *Orchestrator) stopCurrent(name string) (hadProc, wasDocker bool, err error) {
	o.mu.Lock()
	cmd, hadProc := o.procs[name]
	wasDocker = o.dockerNodes[name]
	o.mu.Unlock()

	if hadProc && cmd.Process != nil {
		if err := o.killGroup(cmd.Process.Pid, syscall.SIGKILL); err != nil {
			return hadProc, wasDocker, fmt.Errorf("stop previous process (pid %d): %w", cmd.Process.Pid, err)
		}
	}
	if hadProc {
		o.mu.Lock()
		delete(o.procs, name)
		o.mu.Unlock()
	}
	if wasDocker {
		if err := o.removeDockerContainer(name); err != nil {
			return hadProc, wasDocker, fmt.Errorf("stop previous container: %w", err)
		}
		o.mu.Lock()
		delete(o.dockerNodes, name)
		o.mu.Unlock()
	}
	return hadProc, wasDocker, nil
}

// lockService acquires (lazily creating) the per-service mutex for name and
// returns a func that releases it. Callers should defer the returned func
// immediately. See the serviceLocks field comment for why this exists.
func (o *Orchestrator) lockService(name string) func() {
	o.mu.Lock()
	l, ok := o.serviceLocks[name]
	if !ok {
		l = &sync.Mutex{}
		o.serviceLocks[name] = l
	}
	o.mu.Unlock()
	l.Lock()
	return l.Unlock
}

// Up starts every active service and database in dependency order, gating
// each on health before moving to the next. A node that fails to start
// (build failure, start failure, health timeout) is marked StatusFailed
// (see fail) and Up moves on to the next node rather than aborting —
// everything already running, and every independent branch of the
// topology, is left up. Any node depending (directly or transitively) on
// a failed one is itself marked StatusFailed and skipped without being
// attempted, so it doesn't sit out its own health timeout only to fail
// for the same reason. The returned error, when non-nil, joins one error
// per failed-or-skipped node — it does not mean nothing is running; check
// States() for that. Callers that want an all-or-nothing Up should call
// Down themselves when this returns non-nil.
func (o *Orchestrator) Up(ctx context.Context) error {
	active := o.activeServices()
	order, err := o.topoOrder(active)
	if err != nil {
		return err
	}

	// Gateways bind first: every upstream is a static 127.0.0.1:<port>, so
	// nothing about service readiness matters, and a port that can't bind
	// fails Up before any process is spawned. Like wireProxy this runs
	// once per Up — Restart/Flip never touch it.
	if err := o.wireGateways(); err != nil {
		return err
	}

	failed := map[string]bool{}
	var errs []error

	for _, name := range order {
		if o.testStartHook != nil {
			o.testStartHook(name)
		}
		if _, ok := active[name]; ok {
			svc, err := o.resolve(name)
			if err != nil {
				o.fail(name, err)
				failed[name] = true
				errs = append(errs, fmt.Errorf("orchestrator: %s: %w", name, err))
				continue
			}
			// Checked after resolve, not before: a variant can override
			// DependsOn, so the dependency set to check is the resolved
			// one, not active[name]'s pre-variant config.Service.
			if dep, skip := firstFailedDep(svc.DependsOn, failed); skip {
				err := fmt.Errorf("orchestrator: %s: skipped: dependency %s failed to start", name, dep)
				o.fail(name, err)
				failed[name] = true
				errs = append(errs, err)
				continue
			}
			o.logf("orchestrator: starting service %s", name)
			if err := o.startService(ctx, name, svc); err != nil {
				failed[name] = true
				errs = append(errs, err)
				continue
			}
			// Proxy wiring is a one-time thing per Up: px.Serve binds a
			// listener with no way to release it, so Restart must not
			// call this again — the existing listener keeps forwarding
			// to the service's (static) port across restarts.
			if err := o.wireProxy(name, svc); err != nil {
				failed[name] = true
				errs = append(errs, err)
				continue
			}
			continue
		}
		if db, ok := o.cfg.Databases[name]; ok {
			o.logf("orchestrator: starting database %s", name)
			if err := o.startDatabase(ctx, name, db); err != nil {
				failed[name] = true
				errs = append(errs, err)
			}
			continue
		}
	}

	// on_ready only runs once every active node came up clean — a partial
	// stack (see the per-node failure handling above) isn't "ready" in the
	// sense a seed script or postinstall step cares about.
	if len(errs) == 0 {
		if err := o.runOnReady(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// runOnReady runs cfg.OnReady, once, after Up has confirmed every active
// node clean: cfg.OnReady.Seeds in declared order (the same mechanism a
// manual seed uses), then cfg.OnReady.Run if set. Stops at the first
// failure — a seed or postinstall step that only makes sense given the
// ones before it succeeded shouldn't run against a stack that hasn't been
// prepared the way it expects.
func (o *Orchestrator) runOnReady(ctx context.Context) error {
	for _, name := range o.cfg.OnReady.Seeds {
		o.logf("orchestrator: on_ready: seeding %s", name)
		if _, err := o.Seed(ctx, name); err != nil {
			return fmt.Errorf("orchestrator: on_ready: seed %s: %w", name, err)
		}
	}
	if o.cfg.OnReady.Run != "" {
		o.logf("orchestrator: on_ready: running postinstall step")
		logPath := filepath.Join(o.opts.LogDir, "on-ready.log")
		if err := runShellStep("on_ready hook", o.cfg.OnReady.Run, o.cfg.Dir, logPath); err != nil {
			return fmt.Errorf("orchestrator: on_ready: %w", err)
		}
	}
	return nil
}

// firstFailedDep returns the first of deps present in failed, so a service
// whose dependency didn't come up can be marked failed and skipped without
// attempting to start it — see the Up doc comment.
func firstFailedDep(deps []string, failed map[string]bool) (string, bool) {
	for _, d := range deps {
		if failed[d] {
			return d, true
		}
	}
	return "", false
}

// Down tears down every process and container this Orchestrator started.
// Individual failures are collected and joined rather than aborting early,
// so one stuck node doesn't strand the rest.
func (o *Orchestrator) Down() error {
	// Union of every name ever tracked as native or docker, taken up front
	// so Down knows what to visit. The actual placement to tear down for
	// each name is re-read under that name's lock below, not from this
	// snapshot — a concurrent Flip/Restart may have changed it since.
	o.mu.Lock()
	names := make(map[string]bool, len(o.procs)+len(o.dockerNodes))
	for k := range o.procs {
		names[k] = true
	}
	for k := range o.dockerNodes {
		names[k] = true
	}
	o.mu.Unlock()

	var errs []error
	for name := range names {
		func() {
			// Blocks until any in-flight Flip/Restart on name finishes, so
			// Down never races a same-service operation past it and misses
			// (or double-tears-down) whichever placement is actually live.
			unlock := o.lockService(name)
			defer unlock()

			o.mu.Lock()
			cmd, hasProc := o.procs[name]
			isDocker := o.dockerNodes[name]
			o.mu.Unlock()

			if hasProc {
				if cmd.Process != nil {
					if err := o.killGroup(cmd.Process.Pid, syscall.SIGKILL); err != nil {
						errs = append(errs, fmt.Errorf("%s: kill: %w", name, err))
					}
				}
				o.mu.Lock()
				delete(o.procs, name)
				o.mu.Unlock()
			}
			if isDocker {
				if err := o.removeDockerContainer(name); err != nil {
					errs = append(errs, fmt.Errorf("%s: %w", name, err))
				}
				o.mu.Lock()
				delete(o.dockerNodes, name)
				o.mu.Unlock()
			}
			if hasProc || isDocker {
				o.setStatus(name, StatusStopped, "")
			}
		}()
	}
	return errors.Join(errs...)
}

// Restart stops name's current process/container, if any, re-runs its
// build when stale, and starts it fresh (health-gated; proxy wiring from
// Up is left in place — see the comment on the Up/wireProxy call site).
//
// If stopping the previous instance fails (a genuine error — killGroup
// already treats "no such process" as success, so a returned error means
// something like EPERM), Restart records it and aborts rather than
// starting a replacement over a possibly-still-live predecessor on the
// same port.
func (o *Orchestrator) Restart(ctx context.Context, name string) error {
	if _, ok := o.activeServices()[name]; !ok {
		return fmt.Errorf("orchestrator: restart %q: not an active service", name)
	}
	svc, err := o.resolve(name)
	if err != nil {
		return fmt.Errorf("orchestrator: restart %q: %w", name, err)
	}

	// Serialize against any concurrent Flip/Restart/Down teardown on this
	// same service — see the serviceLocks field comment. Held across the
	// whole read-act-mutate span below, not just the map accesses.
	unlock := o.lockService(name)
	defer unlock()

	o.mu.Lock()
	cmd, hasProc := o.procs[name]
	isDocker := o.dockerNodes[name]
	o.mu.Unlock()

	// Restart must preserve whichever placement is currently running (it
	// may have been flipped since Up), not recompute the config default —
	// otherwise a Restart after Flip would silently flip the service back.
	// With neither tracked (first Restart before any Up), fall back to the
	// config default.
	placement := defaultPlacement(svc)
	switch {
	case isDocker:
		placement = "docker"
	case hasProc:
		placement = "native"
	}

	if hasProc && cmd.Process != nil {
		if err := o.killGroup(cmd.Process.Pid, syscall.SIGKILL); err != nil {
			wrapped := fmt.Errorf("kill previous instance (pid %d): %w", cmd.Process.Pid, err)
			o.fail(name, wrapped)
			return fmt.Errorf("orchestrator: restart %q: %w", name, wrapped)
		}
		o.mu.Lock()
		delete(o.procs, name)
		o.mu.Unlock()
	}
	if isDocker {
		if err := o.removeDockerContainer(name); err != nil {
			wrapped := fmt.Errorf("remove previous container: %w", err)
			o.fail(name, wrapped)
			return fmt.Errorf("orchestrator: restart %q: %w", name, wrapped)
		}
		o.mu.Lock()
		delete(o.dockerNodes, name)
		o.mu.Unlock()
	}

	return o.startServiceAs(ctx, name, svc, placement)
}

// Stop tears down name's currently running process or container, if any,
// and leaves it stopped — unlike Restart, it does not start a replacement.
// The service stays part of the active profile set (o.active is untouched,
// same as Down); only its live process/container goes away, so a later
// Restart brings it back exactly like a first-time start. A no-op, not an
// error, when nothing is currently running.
func (o *Orchestrator) Stop(name string) error {
	if _, ok := o.activeServices()[name]; !ok {
		return fmt.Errorf("orchestrator: stop %q: not an active service", name)
	}

	// Serialize against any concurrent Flip/Restart/Stop/Down teardown on
	// this same service — see the serviceLocks field comment.
	unlock := o.lockService(name)
	defer unlock()

	o.mu.Lock()
	cmd, hasProc := o.procs[name]
	isDocker := o.dockerNodes[name]
	o.mu.Unlock()

	if hasProc && cmd.Process != nil {
		if err := o.killGroup(cmd.Process.Pid, syscall.SIGKILL); err != nil {
			wrapped := fmt.Errorf("kill: %w", err)
			o.fail(name, wrapped)
			return fmt.Errorf("orchestrator: stop %q: %w", name, wrapped)
		}
		o.mu.Lock()
		delete(o.procs, name)
		o.mu.Unlock()
	}
	if isDocker {
		if err := o.removeDockerContainer(name); err != nil {
			wrapped := fmt.Errorf("remove container: %w", err)
			o.fail(name, wrapped)
			return fmt.Errorf("orchestrator: stop %q: %w", name, wrapped)
		}
		o.mu.Lock()
		delete(o.dockerNodes, name)
		o.mu.Unlock()
	}
	if hasProc || isDocker {
		o.setStatus(name, StatusStopped, "")
	}
	return nil
}

// States returns a snapshot of every known node, sorted by name.
func (o *Orchestrator) States() []ServiceState {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]ServiceState, 0, len(o.states))
	for _, s := range o.states {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Service returns name's current state, if known.
func (o *Orchestrator) Service(name string) (ServiceState, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	s, ok := o.states[name]
	if !ok {
		return ServiceState{}, false
	}
	return *s, true
}

// --- node startup ---

// startService starts svc in its default placement: native when it
// declares run (docker, if also declared, is the flippable alternate —
// see defaultPlacement), else docker.
func (o *Orchestrator) startService(ctx context.Context, name string, svc config.Service) error {
	return o.startServiceAs(ctx, name, svc, defaultPlacement(svc))
}

// defaultPlacement is the placement a service starts in until explicitly
// flipped (Flip): native whenever `run` is configured — matching the spec
// scenario "flips a native service to container placement" — and docker
// only when `run` is absent (docker-only services, unchanged from before
// Flip existed).
func defaultPlacement(svc config.Service) string {
	if svc.Run != "" {
		return "native"
	}
	return "docker"
}

// startServiceAs starts svc under the given placement ("native" or
// "docker"), regardless of which placement(s) svc declares — the caller
// (startService, Restart, Flip) is responsible for picking a placement svc
// actually supports.
func (o *Orchestrator) startServiceAs(ctx context.Context, name string, svc config.Service, placement string) error {
	variant := o.currentVariant(name)
	o.setState(name, func(s *ServiceState) {
		s.Placement = placement
		s.ProxyPort = svc.Proxy
		s.Variant = variant
		s.PID = 0 // stale from a previous placement until the native branch below sets it
	})

	workDir := resolveDir(o.cfg.Dir, svc.Dir)

	if svc.Build != "" {
		o.setStatus(name, StatusBuilding, "")
		// Stamps are per variant: the stub being freshly built says
		// nothing about whether the monolith's build is current.
		stampName := name
		if variant != "" {
			stampName = name + "." + variant
		}
		stampPath := filepath.Join(o.opts.LogDir, stampName+".buildstamp")
		stale, err := buildStale(stampPath, workDir, svc.Watch)
		if err != nil {
			o.fail(name, err)
			return fmt.Errorf("orchestrator: %s: check build staleness: %w", name, err)
		}
		if stale {
			logPath := filepath.Join(o.opts.LogDir, name+".log")
			o.logf("orchestrator: building %s (output: %s)", name, logPath)
			buildStart := time.Now()
			if err := runShellStep("build", svc.Build, workDir, logPath); err != nil {
				o.fail(name, err)
				return fmt.Errorf("orchestrator: %s: build: %w", name, err)
			}
			o.logf("orchestrator: built %s in %s", name, time.Since(buildStart).Round(time.Millisecond))
			if err := touchStamp(stampPath); err != nil {
				o.fail(name, err)
				return fmt.Errorf("orchestrator: %s: write build stamp: %w", name, err)
			}
		}
	}

	o.setStatus(name, StatusStarting, "")

	// Computed once, unconditionally: both placements can write here (a
	// docker-placed service still gets its build output logged above), and
	// the on_healthy hook below needs it regardless of placement too.
	logPath := filepath.Join(o.opts.LogDir, name+".log")

	if placement == "docker" {
		if svc.Docker == nil {
			err := fmt.Errorf("no docker placement configured")
			o.fail(name, err)
			return fmt.Errorf("orchestrator: %s: %w", name, err)
		}
		if err := dockerRunService(name, svc.Docker); err != nil {
			o.fail(name, err)
			return fmt.Errorf("orchestrator: %s: %w", name, err)
		}
		o.mu.Lock()
		o.dockerNodes[name] = true
		o.mu.Unlock()
	} else {
		if svc.Run == "" {
			err := fmt.Errorf("no native run command configured")
			o.fail(name, err)
			return fmt.Errorf("orchestrator: %s: %w", name, err)
		}
		cmd, err := startNativeProcess(svc.Run, workDir, svc.Env, logPath)
		if err != nil {
			o.fail(name, err)
			return fmt.Errorf("orchestrator: %s: %w", name, err)
		}
		o.mu.Lock()
		o.procs[name] = cmd
		o.mu.Unlock()
		o.setState(name, func(s *ServiceState) { s.PID = cmd.Process.Pid })
	}

	o.setState(name, func(s *ServiceState) { s.Port = svc.Port })

	if err := o.gateHealth(ctx, name, svc.Health, svc.Port, placement == "docker", svc.StartupTimeoutS); err != nil {
		o.fail(name, err)
		return fmt.Errorf("orchestrator: %s: %w", name, err)
	}

	if svc.OnHealthy != "" {
		o.logf("orchestrator: %s: healthy — running on_healthy hook", name)
		hookStart := time.Now()
		if err := runShellStep("on_healthy hook", svc.OnHealthy, workDir, logPath); err != nil {
			o.fail(name, err)
			return fmt.Errorf("orchestrator: %s: on_healthy: %w", name, err)
		}
		o.logf("orchestrator: %s: on_healthy hook finished in %s", name, time.Since(hookStart).Round(time.Millisecond))
	}

	o.setState(name, func(s *ServiceState) {
		s.Status = StatusHealthy
		s.StartedAt = time.Now()
		s.LastErr = ""
	})
	return nil
}

// wireProxy fronts svc with the intercepting proxy when it wants one
// (Proxy > 0). Called once per service, from Up only — see the call site.
func (o *Orchestrator) wireProxy(name string, svc config.Service) error {
	if svc.Proxy <= 0 {
		return nil
	}
	o.mu.Lock()
	already := o.wired[name]
	o.mu.Unlock()
	if already {
		return nil
	}
	upstream := fmt.Sprintf("http://127.0.0.1:%d", svc.Port)
	if _, err := o.px.Serve(proxy.Target{
		Name:     name,
		Listen:   fmt.Sprintf("127.0.0.1:%d", svc.Proxy),
		Upstream: upstream,
	}); err != nil {
		o.fail(name, err)
		return fmt.Errorf("orchestrator: %s: proxy wiring: %w", name, err)
	}
	o.mu.Lock()
	o.wired[name] = true
	o.mu.Unlock()
	return nil
}

// wireGateways binds one routing listener per configured gateway, each
// route forwarding onto the target's resolved port (config.RoutablePort:
// proxy port if any, else real port; stub port). Gateways are static
// listeners, not supervised nodes, so they have no ServiceState.
func (o *Orchestrator) wireGateways() error {
	names := make([]string, 0, len(o.cfg.Gateways))
	for name := range o.cfg.Gateways {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		gw := o.cfg.Gateways[name]
		routes := make([]proxy.Route, 0, len(gw.Routes))
		for _, r := range gw.Routes {
			port, _, ok := o.cfg.RoutablePort(r.Service)
			if !ok {
				// Validate rejects this; guard anyway so a hand-built
				// Config can't produce a route to port 0.
				return fmt.Errorf("orchestrator: gateway %s: route %q: %q has no routable port", name, r.Prefix, r.Service)
			}
			route := proxy.Route{
				Prefix:      r.Prefix,
				Upstream:    fmt.Sprintf("http://127.0.0.1:%d", port),
				StripPrefix: r.StripPrefix,
				Rewrite:     r.Rewrite,
			}
			if r.Regex != "" {
				// Validate already confirmed this compiles; guard anyway
				// so a hand-built Config can't panic the orchestrator.
				re, err := regexp.Compile(r.Regex)
				if err != nil {
					return fmt.Errorf("orchestrator: gateway %s: route %q: invalid regex: %w", name, r.Regex, err)
				}
				route.Regex = re
			}
			routes = append(routes, route)
		}
		var cors *proxy.CORSPolicy
		if gw.CORS != nil {
			cors = &proxy.CORSPolicy{
				AllowOrigins:     gw.CORS.AllowOrigins,
				AllowCredentials: gw.CORS.AllowCredentials,
				AllowMethods:     gw.CORS.AllowMethods,
				AllowHeaders:     gw.CORS.AllowHeaders,
				MaxAgeSeconds:    gw.CORS.MaxAgeSeconds,
			}
		}
		if _, err := o.px.Serve(proxy.Target{
			Name:   name,
			Listen: fmt.Sprintf("127.0.0.1:%d", gw.Port),
			Routes: routes,
			CORS:   cors,
		}); err != nil {
			return fmt.Errorf("orchestrator: gateway %s: wiring: %w", name, err)
		}
		o.logf("orchestrator: gateway %s listening on 127.0.0.1:%d (%d routes)", name, gw.Port, len(routes))
	}
	return nil
}

func (o *Orchestrator) startDatabase(ctx context.Context, name string, db config.Database) error {
	o.setState(name, func(s *ServiceState) { s.Placement = "docker" })
	o.setStatus(name, StatusStarting, "")

	// Three states, one convergence point. A database container is the one
	// thing in a stack that is expensive to recreate and usually wanted
	// intact: postgres and localstack are slow to start, and their whole
	// value between runs is the data already in them. So an existing
	// ensemble-<name> container is adopted rather than treated as an
	// obstacle — `up` after `up` is a normal thing to do.
	//
	// Only OUR OWN containers are ever touched. dockerContainerName scopes
	// every lookup here to the "ensemble-" prefix, so a developer's own
	// container of the same nominal service is never adopted, started, or
	// otherwise interfered with; it surfaces as a port conflict in preflight,
	// which is the correct outcome for something ensemble does not own.
	exists, running, err := dockerContainerState(ctx, name)
	if err != nil {
		o.fail(name, err)
		return fmt.Errorf("orchestrator: %s: %w", name, err)
	}
	switch {
	case !exists:
		if err := dockerRunDatabase(name, db); err != nil {
			o.fail(name, err)
			return fmt.Errorf("orchestrator: %s: %w", name, err)
		}
	case running:
		// Already up: nothing to do but verify it, which the health gate
		// below does anyway. Logged because "ensemble didn't start this"
		// is exactly the thing someone debugging stale data needs to know.
		o.logf("orchestrator: database %s: reusing running container %s", name, dockerContainerName(name))
	default:
		o.logf("orchestrator: database %s: starting existing container %s", name, dockerContainerName(name))
		if err := dockerStart(ctx, name); err != nil {
			o.fail(name, err)
			return fmt.Errorf("orchestrator: %s: %w", name, err)
		}
	}
	o.mu.Lock()
	o.dockerNodes[name] = true
	o.mu.Unlock()

	if err := o.gateDatabaseHealth(ctx, name, db); err != nil {
		o.fail(name, err)
		return fmt.Errorf("orchestrator: %s: %w", name, err)
	}

	o.setState(name, func(s *ServiceState) {
		s.Status = StatusHealthy
		s.Port = db.Port
		s.StartedAt = time.Now()
		s.LastErr = ""
	})
	return nil
}

// gateDatabaseHealth gates a managed database container on the docker
// "running" check plus a real readiness check: o.DBReady when the
// orchestrator has one wired, else the legacy bare TCP dial (see the
// DBReady field comment for why the dial alone is insufficient — task 3.6,
// defect D3). Uses the same overall timeout budget as gateHealth
// (o.opts.HealthTimeout).
//
// Services are untouched by this — they still go through gateHealth, whose
// TCP-dial semantics remain correct there (a native process either has its
// port open or it doesn't; there's no docker published-port proxy in the
// way to mask the difference).
func (o *Orchestrator) gateDatabaseHealth(ctx context.Context, name string, db config.Database) error {
	if err := pollDockerRunning(ctx, name, o.opts.HealthTimeout); err != nil {
		return err
	}
	if db.Port <= 0 {
		return nil
	}
	if o.DBReady != nil {
		return pollUntil(ctx, o.opts.HealthTimeout, func() (bool, error) {
			err := o.DBReady(ctx, name, db)
			return err == nil, err
		})
	}
	return pollTCP(ctx, fmt.Sprintf("127.0.0.1:%d", db.Port), o.opts.HealthTimeout)
}

// gateHealth implements the health-gate rule: a Health path is polled
// until 2xx or timeout; with no Health path, a docker node must show its
// container running, and (either placement) a Port > 0 must accept a TCP
// dial. No Health and no Port is trivially healthy — the process/container
// having started successfully is all there is to check.
//
// startupTimeoutS is the service's config.Service.StartupTimeoutS: 0 uses
// o.opts.HealthTimeout (the 30s-default budget every other service gates
// on), a positive value overrides it for this service alone — see
// resolveHealthTimeout.
func (o *Orchestrator) gateHealth(ctx context.Context, name, healthPath string, port int, isDocker bool, startupTimeoutS int) error {
	timeout := o.resolveHealthTimeout(startupTimeoutS)
	if healthPath != "" {
		url := fmt.Sprintf("http://127.0.0.1:%d%s", port, healthPath)
		return pollHealth(ctx, url, timeout)
	}
	if isDocker {
		if err := pollDockerRunning(ctx, name, timeout); err != nil {
			return err
		}
	}
	if port > 0 {
		addr := fmt.Sprintf("127.0.0.1:%d", port)
		return pollTCP(ctx, addr, timeout)
	}
	return nil
}

// resolveHealthTimeout returns the per-service override (startupTimeoutS,
// config.Service.StartupTimeoutS) as a duration when set, else falls back
// to the orchestrator-wide o.opts.HealthTimeout. Validate() rejects a
// negative override, so 0 is the only "unset" value reaching here.
func (o *Orchestrator) resolveHealthTimeout(startupTimeoutS int) time.Duration {
	if startupTimeoutS > 0 {
		return time.Duration(startupTimeoutS) * time.Second
	}
	return o.opts.HealthTimeout
}

// --- build-if-stale ---

// buildStale reports whether svc.Build should run: the stamp is missing,
// there are no watch globs (always rebuild when Build is set), or some
// watched file's mtime is newer than the stamp's.
func buildStale(stampPath, workDir string, watch []string) (bool, error) {
	info, err := os.Stat(stampPath)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if len(watch) == 0 {
		return true, nil
	}
	stampTime := info.ModTime()
	for _, pattern := range watch {
		matches, err := filepath.Glob(filepath.Join(workDir, pattern))
		if err != nil {
			return false, fmt.Errorf("watch glob %q: %w", pattern, err)
		}
		for _, m := range matches {
			fi, err := os.Stat(m)
			if err != nil {
				continue
			}
			if fi.ModTime().After(stampTime) {
				return true, nil
			}
		}
	}
	return false, nil
}

// shellStepTailBytes bounds how much output a failed shell step's error
// carries — enough for the compiler's (or seed script's) complaint, not
// the whole log.
const shellStepTailBytes = 4 * 1024

// runShellStep runs command in workDir under `/bin/sh -c`, streaming its
// output to logPath (the service's own log, appended under a header naming
// the step) so a multi-minute step (a `docker build`, `gradle`, or seed
// script) is visible as it happens rather than only once it ends. label
// names the kind of step for the log header and error text — "build",
// "on_healthy hook", "on_ready hook". A failure's error carries the last
// shellStepTailBytes of output, so ServiceState.LastErr still says why.
func runShellStep(label, command, workDir, logPath string) error {
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return fmt.Errorf("create log dir: %w", err)
	}
	logFile, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open log %s: %w", logPath, err)
	}
	defer logFile.Close()
	fmt.Fprintf(logFile, "=== %s: %s ===\n", label, command)

	tail := &tailBuffer{limit: shellStepTailBytes}
	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.Dir = workDir
	cmd.Stdout = io.MultiWriter(logFile, tail)
	cmd.Stderr = cmd.Stdout
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(logFile, "=== %s failed: %v ===\n", label, err)
		return fmt.Errorf("%s: %q: %w: %s", label, command, err, strings.TrimSpace(tail.String()))
	}
	fmt.Fprintf(logFile, "=== %s ok ===\n", label)
	return nil
}

// tailBuffer keeps the last limit bytes written to it.
type tailBuffer struct {
	buf   []byte
	limit int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.limit {
		t.buf = t.buf[len(t.buf)-t.limit:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string { return string(t.buf) }

// touchStamp records "now" as the build's completion time.
func touchStamp(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	now := time.Now()
	if err := os.Chtimes(path, now, now); err == nil {
		return nil
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	return f.Close()
}

// --- dependency ordering ---

// topoOrder returns the given services plus every database in
// dependency-first order (a dependency always precedes its dependents). A
// depends_on cycle is reported as an error naming every service in the
// cycle.
func (o *Orchestrator) topoOrder(active map[string]config.Service) ([]string, error) {
	nodes := map[string]bool{}
	for name := range active {
		nodes[name] = true
	}
	for name := range o.cfg.Databases {
		nodes[name] = true
	}

	deps := map[string][]string{}
	for name, svc := range active {
		for _, d := range svc.DependsOn {
			if nodes[d] {
				deps[name] = append(deps[name], d)
			}
		}
	}
	for _, ds := range deps {
		sort.Strings(ds)
	}

	names := make([]string, 0, len(nodes))
	for n := range nodes {
		names = append(names, n)
	}
	sort.Strings(names)

	const (
		white = iota
		gray
		black
	)
	color := map[string]int{}
	var order []string
	var stack []string
	var cycleErr error

	var visit func(n string)
	visit = func(n string) {
		color[n] = gray
		stack = append(stack, n)
		for _, d := range deps[n] {
			if cycleErr != nil {
				return
			}
			switch color[d] {
			case white:
				visit(d)
			case gray:
				idx := indexOf(stack, d)
				cyc := append(append([]string{}, stack[idx:]...), d)
				cycleErr = fmt.Errorf("orchestrator: dependency cycle: %s", strings.Join(cyc, " -> "))
			}
		}
		if cycleErr != nil {
			return
		}
		color[n] = black
		stack = stack[:len(stack)-1]
		order = append(order, n)
	}

	for _, n := range names {
		if color[n] == white {
			visit(n)
			if cycleErr != nil {
				return nil, cycleErr
			}
		}
	}
	return order, nil
}

func indexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}

// --- state helpers ---

func (o *Orchestrator) setState(name string, mut func(*ServiceState)) {
	o.mu.Lock()
	defer o.mu.Unlock()
	s, ok := o.states[name]
	if !ok {
		s = &ServiceState{Name: name}
		o.states[name] = s
	}
	mut(s)
}

func (o *Orchestrator) setStatus(name string, status Status, lastErr string) {
	o.setState(name, func(s *ServiceState) {
		s.Status = status
		if lastErr != "" {
			s.LastErr = lastErr
		}
	})
}

func (o *Orchestrator) fail(name string, err error) {
	o.setStatus(name, StatusFailed, err.Error())
}

func (o *Orchestrator) logf(format string, args ...any) {
	if o.opts.Logf != nil {
		o.opts.Logf(format, args...)
	}
}
