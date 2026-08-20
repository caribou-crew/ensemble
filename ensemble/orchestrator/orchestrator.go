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

	"github.com/ensemble-dev/ensemble/core/proxy"
	"github.com/ensemble-dev/ensemble/ensemble/config"
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
	Name      string
	Status    Status
	Placement string // "native" | "docker"
	PID       int    // native only; 0 for docker
	ProxyPort int
	Port      int
	StartedAt time.Time
	LastErr   string
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

	// testStartHook, set only from tests, is called with each node's name
	// the moment Up begins starting it — proof of dependency order without
	// depending on process-timing.
	testStartHook func(name string)
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
		cfg:         cfg,
		px:          px,
		opts:        opts,
		states:      map[string]*ServiceState{},
		procs:       map[string]*exec.Cmd{},
		dockerNodes: map[string]bool{},
	}
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
	o.mu.Lock()
	procs := make(map[string]*exec.Cmd, len(o.procs))
	for k, v := range o.procs {
		procs[k] = v
	}
	dockerNodes := make(map[string]bool, len(o.dockerNodes))
	for k, v := range o.dockerNodes {
		dockerNodes[k] = v
	}
	o.mu.Unlock()

	var errs []error
	for name, cmd := range procs {
		if cmd.Process != nil {
			if err := killProcessGroup(cmd.Process.Pid, syscall.SIGKILL); err != nil {
				errs = append(errs, fmt.Errorf("%s: kill: %w", name, err))
			}
		}
		o.mu.Lock()
		delete(o.procs, name)
		o.mu.Unlock()
		o.setStatus(name, StatusStopped, "")
	}
	for name := range dockerNodes {
		if err := dockerRemove(name); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
		}
		o.mu.Lock()
		delete(o.dockerNodes, name)
		o.mu.Unlock()
		o.setStatus(name, StatusStopped, "")
	}
	return errors.Join(errs...)
}

// Restart stops name's current process/container, if any, re-runs its
// build when stale, and starts it fresh (health-gated, proxy re-wired).
func (o *Orchestrator) Restart(ctx context.Context, name string) error {
	active := o.cfg.ServicesForProfiles(o.opts.Profiles)
	svc, ok := active[name]
	if !ok {
		return fmt.Errorf("orchestrator: restart %q: not an active service", name)
	}

	o.mu.Lock()
	cmd, hasProc := o.procs[name]
	isDocker := o.dockerNodes[name]
	o.mu.Unlock()

	if hasProc && cmd.Process != nil {
		_ = killProcessGroup(cmd.Process.Pid, syscall.SIGKILL)
		o.mu.Lock()
		delete(o.procs, name)
		o.mu.Unlock()
	}
	if isDocker {
		_ = dockerRemove(name)
		o.mu.Lock()
		delete(o.dockerNodes, name)
		o.mu.Unlock()
	}

	return o.startService(ctx, name, svc)
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

func (o *Orchestrator) startService(ctx context.Context, name string, svc config.Service) error {
	placement := "native"
	if svc.Docker != nil {
		placement = "docker"
	}
	o.setState(name, func(s *ServiceState) {
		s.Placement = placement
		s.ProxyPort = svc.Proxy
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

	if svc.Docker != nil {
		if err := dockerRunService(name, svc.Docker); err != nil {
			o.fail(name, err)
			return fmt.Errorf("orchestrator: %s: %w", name, err)
		}
		o.mu.Lock()
		o.dockerNodes[name] = true
		o.mu.Unlock()
	} else {
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

	if err := o.gateHealth(ctx, name, svc.Health, svc.Port, svc.Docker != nil); err != nil {
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
