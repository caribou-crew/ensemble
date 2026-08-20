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
	"os"
	"os/exec"
	"path/filepath"
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
	StartedAt time.Time `json:"startedAt,omitzero"`
	LastErr   string    `json:"lastErr,omitempty"`
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
}

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
		serviceLocks:          map[string]*sync.Mutex{},
		killGroup:             killProcessGroup,
		removeDockerContainer: dockerRemove,
	}
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

// Up starts every active service and database in dependency order,
// gating each on health before moving to the next. The first failure
// (cycle, build failure, start failure, health timeout) stops Up and is
// returned naming the offending node; nodes already started are left
// running (call Down to tear them down).
func (o *Orchestrator) Up(ctx context.Context) error {
	order, err := o.topoOrder()
	if err != nil {
		return err
	}
	active := o.cfg.ServicesForProfiles(o.opts.Profiles)

	for _, name := range order {
		if o.testStartHook != nil {
			o.testStartHook(name)
		}
		if svc, ok := active[name]; ok {
			o.logf("orchestrator: starting service %s", name)
			if err := o.startService(ctx, name, svc); err != nil {
				return err
			}
			// Proxy wiring is a one-time thing per Up: px.Serve binds a
			// listener with no way to release it, so Restart must not
			// call this again — the existing listener keeps forwarding
			// to the service's (static) port across restarts.
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
			continue
		}
	}
	return nil
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
	active := o.cfg.ServicesForProfiles(o.opts.Profiles)
	svc, ok := active[name]
	if !ok {
		return fmt.Errorf("orchestrator: restart %q: not an active service", name)
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
	o.setState(name, func(s *ServiceState) {
		s.Placement = placement
		s.ProxyPort = svc.Proxy
		s.PID = 0 // stale from a previous placement until the native branch below sets it
	})

	workDir := resolveDir(o.cfg.Dir, svc.Dir)

	if svc.Build != "" {
		o.setStatus(name, StatusBuilding, "")
		stampPath := filepath.Join(o.opts.LogDir, name+".buildstamp")
		stale, err := buildStale(stampPath, workDir, svc.Watch)
		if err != nil {
			o.fail(name, err)
			return fmt.Errorf("orchestrator: %s: check build staleness: %w", name, err)
		}
		if stale {
			if err := runBuild(svc.Build, workDir); err != nil {
				o.fail(name, err)
				return fmt.Errorf("orchestrator: %s: build: %w", name, err)
			}
			if err := touchStamp(stampPath); err != nil {
				o.fail(name, err)
				return fmt.Errorf("orchestrator: %s: write build stamp: %w", name, err)
			}
		}
	}

	o.setStatus(name, StatusStarting, "")

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
		logPath := filepath.Join(o.opts.LogDir, name+".log")
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

	if err := o.gateHealth(ctx, name, svc.Health, svc.Port, placement == "docker"); err != nil {
		o.fail(name, err)
		return fmt.Errorf("orchestrator: %s: %w", name, err)
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
	upstream := fmt.Sprintf("http://127.0.0.1:%d", svc.Port)
	if _, err := o.px.Serve(proxy.Target{
		Name:     name,
		Listen:   fmt.Sprintf("127.0.0.1:%d", svc.Proxy),
		Upstream: upstream,
	}); err != nil {
		o.fail(name, err)
		return fmt.Errorf("orchestrator: %s: proxy wiring: %w", name, err)
	}
	return nil
}

func (o *Orchestrator) startDatabase(ctx context.Context, name string, db config.Database) error {
	o.setState(name, func(s *ServiceState) { s.Placement = "docker" })
	o.setStatus(name, StatusStarting, "")

	if err := dockerRunDatabase(name, db); err != nil {
		o.fail(name, err)
		return fmt.Errorf("orchestrator: %s: %w", name, err)
	}
	o.mu.Lock()
	o.dockerNodes[name] = true
	o.mu.Unlock()

	if err := o.gateHealth(ctx, name, "", db.Port, true); err != nil {
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

// gateHealth implements the health-gate rule: a Health path is polled
// until 2xx or timeout; with no Health path, a docker node must show its
// container running, and (either placement) a Port > 0 must accept a TCP
// dial. No Health and no Port is trivially healthy — the process/container
// having started successfully is all there is to check.
func (o *Orchestrator) gateHealth(ctx context.Context, name, healthPath string, port int, isDocker bool) error {
	if healthPath != "" {
		url := fmt.Sprintf("http://127.0.0.1:%d%s", port, healthPath)
		return pollHealth(ctx, url, o.opts.HealthTimeout)
	}
	if isDocker {
		if err := pollDockerRunning(ctx, name, o.opts.HealthTimeout); err != nil {
			return err
		}
	}
	if port > 0 {
		addr := fmt.Sprintf("127.0.0.1:%d", port)
		return pollTCP(ctx, addr, o.opts.HealthTimeout)
	}
	return nil
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

func runBuild(build, workDir string) error {
	cmd := exec.Command("/bin/sh", "-c", build)
	cmd.Dir = workDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%q: %w: %s", build, err, strings.TrimSpace(string(out)))
	}
	return nil
}

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

// topoOrder returns active services and databases in dependency-first
// order (a dependency always precedes its dependents). A depends_on cycle
// is reported as an error naming every service in the cycle.
func (o *Orchestrator) topoOrder() ([]string, error) {
	active := o.cfg.ServicesForProfiles(o.opts.Profiles)

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
