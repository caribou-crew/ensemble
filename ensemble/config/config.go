// Package config parses and validates ensemble.yaml, the user-supplied
// description of a local stack's topology: services, databases, stubs,
// entities, latency defaults, seeds, and profiles.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the parsed contents of ensemble.yaml.
type Config struct {
	Services  map[string]Service  `yaml:"services"`
	Databases map[string]Database `yaml:"databases"`
	Stubs     map[string]Stub     `yaml:"stubs"`
	Gateways  map[string]Gateway  `yaml:"gateways"`
	Entities  map[string]Entity   `yaml:"entities"`
	Latency   Latency             `yaml:"latency"`
	// Datadog configures the optional `ensemble latency from-datadog`/
	// `apply` integration — see DatadogConfig. Nil (no `datadog:` key) is
	// the zero-config path: DD_API_KEY/DD_APP_KEY/DD_SITE are read
	// directly and datadoghq.com/60min defaults apply.
	Datadog  *DatadogConfig      `yaml:"datadog"`
	Seeds    map[string]Seed     `yaml:"seeds"`
	Profiles map[string][]string `yaml:"profiles"`
	Redact   []string            `yaml:"redact"` // extra redaction keys
	OnReady  OnReady             `yaml:"on_ready"`
	// Preflight lists checks that must succeed before `ensemble up` starts
	// anything else — see PreflightCheck. Run in declared order; the first
	// failure aborts the whole command before any process, container, or
	// port bind happens. Empty (the default) runs no checks. Meant for
	// dependencies ensemble itself can't start for you — a container
	// runtime (docker/podman) that isn't running, a VPN, an internal
	// service reachable only over one — that otherwise fail confusingly
	// deep inside orchestrator's first docker/build call.
	Preflight []PreflightCheck `yaml:"preflight"`
	// Readiness configures post-on_ready readiness checks — see the
	// Readiness type and ReadinessChecks() for the parsed checks file it
	// points at. Nil (the default) means no readiness checks are
	// configured; the stack is considered ready as soon as on_ready
	// completes.
	Readiness *Readiness `yaml:"readiness"`
	// readinessChecks caches the parsed contents of Readiness.File —
	// populated by Validate so a config error in the checks file itself
	// (bad YAML, unknown service) is caught at load time, not first use.
	readinessChecks *ReadinessChecksFile `yaml:"-"`
	// latencyProfiles caches every latency.profiles entry's parsed rules
	// file, keyed by profile name — populated by Validate for the same
	// reason readinessChecks is: a bad profile file fails at config-load
	// time, not at first `latency apply`. See Config.LatencyProfile.
	latencyProfiles map[string]*LatencyProfileFile `yaml:"-"`
	// dotenv is the parsed .env file (if any) found next to ensemble.yaml
	// at Load time — kept after ${VAR} expansion (see expandEnvVars) so
	// LookupEnv can resolve names like a datadog: block's api_key_env
	// through the same env-then-.env precedence, without re-reading the
	// file. Nil is a valid state (no .env present) — LookupEnv handles it.
	dotenv map[string]string `yaml:"-"`
	// TraceHeader names a stack's own correlation header (e.g.
	// "x-local-trace-id") — read as a fallback trace id whenever a request
	// carries no real W3C traceparent, so hops still land in one trace
	// instead of scattering across synthetic ones (see
	// core/trace.ResolveInbound, core/proxy.Proxy.TraceHeader). Stack-wide
	// because it's a convention the whole company's services already
	// share, not a per-service setting. Empty (the default) disables the
	// fallback entirely.
	TraceHeader string `yaml:"trace_header"`
	// SourceHeaders names request headers (checked in order, case-insensitive
	// per the HTTP header convention) that let a caller ensemble doesn't
	// manage (a dev-only client, another team's tool) self-declare its name
	// on the request itself — see core/proxy.Proxy.SourceHeaders,
	// core/proxy.CallerHeader. The first header present on a request wins.
	// Empty (the default) falls back to checking only the built-in
	// X-Ensemble-Caller header — set this only if the org already has its
	// own convention (e.g. "x-source-client") to prefer instead/first.
	SourceHeaders []string `yaml:"source_header"`
	// ClientIdentityHeaders names request headers (checked in order,
	// case-insensitive) carrying the name of the CLIENT APPLICATION that
	// sent a request — "web", "ios", "admin" — which lands on
	// trace.Hop.Client and shows in the traffic view. Empty (the default)
	// checks core/proxy.DefaultClientHeaders: x-source-client, then
	// x-local-client.
	//
	// Read this next to source_header and pick deliberately; they are
	// neighbours, not alternatives. source_header answers "which SERVICE
	// called this hop" and is a fallback for missing trace context.
	// client_identity_headers answers "which of our FRONT-ENDS started
	// this", is read on every request whether or not trace context exists,
	// and is validated to an identifier — a value that fails is recorded as
	// "client" and never stored, so nothing a browser puts in the header
	// reaches disk. A stack commonly wants one and not the other.
	ClientIdentityHeaders []string `yaml:"client_identity_headers"`
	Dir                   string   `yaml:"-"` // dir containing the config file (set by Load)
}

// OnReady runs once `ensemble up` has brought every active service and
// database up healthy — a stack-level postinstall step, for work that only
// makes sense once the whole stack is reachable (seeding data that spans
// several services, warming a cache, announcing readiness to an external
// system). Seeds names targets from the `seeds:` map, run in declared
// order through the same mechanism a manual seed uses; Run is a plain
// shell command, executed in Config.Dir after every named seed. Both are
// optional and may be combined; neither runs if Up itself reports any
// node failed.
type OnReady struct {
	Seeds []string `yaml:"seeds"`
	Run   string   `yaml:"run"`
}

// DefaultPreflightTimeoutS applies when a PreflightCheck.TimeoutS is left
// unset (zero) in ensemble.yaml.
const DefaultPreflightTimeoutS = 10

// PreflightCheck is one command that must exit 0 before `ensemble up`
// proceeds — see Config.Preflight. Run executes under `/bin/sh -c`, the
// same convention as OnReady.Run and a service's build/hook commands, so
// it can be a plain binary invocation ("podman info") or a small pipeline.
type PreflightCheck struct {
	// Name identifies the check in progress/error output. Defaults to Run
	// when left unset.
	Name string `yaml:"name"`
	Run  string `yaml:"run"`
	// Message, if set, replaces the command's own output in the failure
	// error — for a check whose stderr/exit status isn't self-explanatory
	// to someone who didn't write it (e.g. "podman info" failing with a
	// generic connection-refused error).
	Message string `yaml:"message"`
	// TimeoutS bounds how long Run may take. 0 (unset) uses
	// DefaultPreflightTimeoutS.
	TimeoutS int `yaml:"timeout_s"`
}

// EffectiveTimeoutS returns TimeoutS, or DefaultPreflightTimeoutS when unset.
func (p PreflightCheck) EffectiveTimeoutS() int {
	if p.TimeoutS > 0 {
		return p.TimeoutS
	}
	return DefaultPreflightTimeoutS
}

// Service describes one process or container ensemble supervises.
type Service struct {
	Dir    string            `yaml:"dir"`
	Build  string            `yaml:"build"`
	Watch  []string          `yaml:"watch"` // globs for build freshness
	Run    string            `yaml:"run"`
	Port   int               `yaml:"port"`  // real service port
	Proxy  int               `yaml:"proxy"` // intercept port (0 = auto-assign later)
	Env    map[string]string `yaml:"env"`
	Health string            `yaml:"health"` // path, e.g. /healthz
	// OnHealthy is a shell command run once each time this service's
	// health gate passes — e.g. a database seed script that only makes
	// sense once this service (and whatever it depends on) is actually
	// reachable. Runs in Dir, the same as Build; a failure here fails the
	// start the same way a Build failure does, before the service is
	// reported healthy.
	OnHealthy string   `yaml:"on_healthy"`
	DependsOn []string `yaml:"depends_on"`
	// CalledBy is a caller-attribution hint for the dashboard's traffic
	// view: who calls this service, used only as a fallback when the real
	// caller can't be identified via W3C trace-context propagation (see
	// core/proxy.Target.CalledBy) — typically because this service is a
	// real/vendored backend with no tracing instrumentation of its own.
	// Left empty, Config.CalledBy derives it from every other service's
	// depends_on instead (see that method). A hop attributed this way is
	// marked "inferred" in the UI, never presented as ground truth.
	CalledBy []string         `yaml:"called_by"`
	Docker   *DockerPlacement `yaml:"docker"`
	Entry    bool             `yaml:"entry"`   // clients call this directly
	Profile  string           `yaml:"profile"` // "" = always on
	// Kind is a free-form label for the dashboard's Services tab (e.g.
	// "stub", "mock", "wip") — ensemble never interprets it, it just badges
	// the row. Unset displays as "service". Named Kind, not Type, to avoid
	// colliding with Database.Type (a validated engine enum — postgres,
	// redis, etc. — a different concept entirely). A variant's own Kind
	// overrides this one only when set (see ResolveService); left unset,
	// the variant inherits the service-level Kind.
	Kind string `yaml:"kind"`
	// StartupTimeoutS overrides Orchestrator.Opts.HealthTimeout (default 30s)
	// for this service's health gate only. 0 = use the default. For a slow
	// starter (a JVM service paying classloading/Spring-context cost on
	// every boot, say) the global default is too tight to raise for
	// everyone else just to accommodate one service.
	StartupTimeoutS int `yaml:"startup_timeout_s"`

	// Variants are named alternative backings for this one logical
	// service — e.g. a small Go stub of a monolith and the monolith
	// itself — sharing Port/Proxy/Health/DependsOn/Entry/Profile but each
	// with its own Dir/Build/Run/Env/Docker. When set, the service-level
	// backing fields above must be empty (see Validate) and Default names
	// the variant `ensemble up` starts. The orchestrator switches between
	// them at runtime the way Flip switches placements: same port, proxy
	// listener untouched.
	Variants map[string]Variant `yaml:"variants"`
	Default  string             `yaml:"default"`
}

// Variant is one backing of a Service: the fields that describe how to
// build and run it, and nothing about what it is on the network.
type Variant struct {
	Dir             string            `yaml:"dir"`
	Build           string            `yaml:"build"`
	Watch           []string          `yaml:"watch"`
	Run             string            `yaml:"run"`
	Env             map[string]string `yaml:"env"`
	Docker          *DockerPlacement  `yaml:"docker"`
	StartupTimeoutS int               `yaml:"startup_timeout_s"`
	// Kind overrides the service-level Kind for this variant only when set
	// — see Service.Kind.
	Kind string `yaml:"kind"`
}

// hasBackingFields reports whether any per-backing field is set at the
// service level — the set a Variant carries.
func (s Service) hasBackingFields() bool {
	return s.Dir != "" || s.Build != "" || len(s.Watch) > 0 || s.Run != "" ||
		len(s.Env) > 0 || s.Docker != nil || s.StartupTimeoutS != 0
}

// VariantNames returns the declared variant names, sorted.
func (s Service) VariantNames() []string {
	names := make([]string, 0, len(s.Variants))
	for n := range s.Variants {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// DefaultVariant is the variant a fresh start uses: Default when set, the
// sole variant when exactly one is declared, else "" (no variants, or an
// invalid config Validate would have rejected).
func (s Service) DefaultVariant() string {
	if s.Default != "" {
		return s.Default
	}
	if len(s.Variants) == 1 {
		for n := range s.Variants {
			return n
		}
	}
	return ""
}

// ResolveService returns the named service flattened to a single backing:
// the variant's Dir/Build/Watch/Run/Env/Docker/StartupTimeoutS overlaid on
// the service's identity fields, with Variants/Default cleared so the
// result looks exactly like a plain service to everything downstream. An
// empty variant means the default. A service without variants is returned
// as-is (variant must then be empty).
func (c *Config) ResolveService(name, variant string) (Service, error) {
	svc, ok := c.Services[name]
	if !ok {
		return Service{}, fmt.Errorf("config: service %q not found", name)
	}
	if len(svc.Variants) == 0 {
		if variant != "" {
			return Service{}, fmt.Errorf("config: service %q has no variants (asked for %q)", name, variant)
		}
		return svc, nil
	}
	if variant == "" {
		variant = svc.DefaultVariant()
	}
	v, ok := svc.Variants[variant]
	if !ok {
		return Service{}, fmt.Errorf("config: service %q has no variant %q (have %s)", name, variant, strings.Join(svc.VariantNames(), ", "))
	}
	out := svc
	out.Dir, out.Build, out.Watch, out.Run = v.Dir, v.Build, v.Watch, v.Run
	out.Env, out.Docker, out.StartupTimeoutS = v.Env, v.Docker, v.StartupTimeoutS
	if v.Kind != "" {
		out.Kind = v.Kind
	}
	out.Variants, out.Default = nil, ""
	return out, nil
}

// DockerPlacement runs a Service as a container instead of a native process.
type DockerPlacement struct {
	Image string            `yaml:"image"`
	Ports []string          `yaml:"ports"`
	Env   map[string]string `yaml:"env"`
	// Args are extra `docker run` flags appended verbatim before the
	// image — `--add-host=host.docker.internal:host-gateway` so a
	// containerized service can reach host-side databases on Linux,
	// `--network`, `-v`, `--platform`, anything ensemble has no field for.
	Args []string `yaml:"args"`
}

// Database is a datastore ensemble provisions alongside services.
// Type defaults from Image when left empty; see Validate.
type Database struct {
	Image string `yaml:"image"`
	Port  int    `yaml:"port"`
	Type  string `yaml:"type"` // postgres|mysql|redis|dynamodb|localstack|http
	// ContainerPort overrides the port the orchestrator publishes Port to,
	// inside the container. Databases are always published to a fixed
	// default port per Type (5432 for postgres, etc — see
	// orchestrator.defaultContainerPorts); this is the escape hatch for an
	// image that listens somewhere else. Zero (the default) means "use the
	// Type table". Purely additive: it changes no meaning of any existing
	// config field.
	ContainerPort int               `yaml:"containerPort"`
	Seed          string            `yaml:"seed"`
	Env           map[string]string `yaml:"env"`
	Services      []string          `yaml:"services"`
	// URL and Headers are meaningful only for Type "http": URL is the base
	// URL of the service's own inspection endpoint (see
	// inspector.NewHTTPDriver), and Headers are sent on every request
	// against it (auth for a protected debug surface).
	URL     string            `yaml:"url"`
	Headers map[string]string `yaml:"headers"`
}

// Stub is a config-defined fake HTTP service.
type Stub struct {
	Port   int         `yaml:"port"`
	Routes []StubRoute `yaml:"routes"`
}

// StubRoute pairs a request matcher with a canned response.
type StubRoute struct {
	Match   StubMatch   `yaml:"match"`
	Respond StubRespond `yaml:"respond"`
}

// StubMatch selects which requests a StubRoute answers.
type StubMatch struct {
	Method string `yaml:"method"`
	Path   string `yaml:"path"`
}

// StubRespond is the canned response for a matched request.
type StubRespond struct {
	Status   int               `yaml:"status"`
	Headers  map[string]string `yaml:"headers"`
	Body     string            `yaml:"body"`
	BodyFile string            `yaml:"body_file"`
	Template bool              `yaml:"template"`
}

// Gateway is a config-defined edge router: one intercept port that fans
// requests out to services and stubs by path prefix, forwarding onto
// ensemble's own resolved ports (see Config.RoutablePort). It fills the
// role a hand-written edge/envoy service otherwise would, and is captured
// as a hop node like any other listener.
type Gateway struct {
	Port   int            `yaml:"port"`
	Routes []GatewayRoute `yaml:"routes"`
	// CORS, when set, makes this gateway add cross-origin response
	// headers and answer preflight OPTIONS requests directly. Absent
	// (nil) by default — fully backward compatible.
	CORS *CORSConfig `yaml:"cors"`
	// ExposeInTraffic opts this gateway INTO being shown as its own hop in
	// the dashboard's Traffic tab. Default false: the Traffic tab collapses
	// "client -> gateway -> target" down to "client -> target", since most
	// users care about the logical call, not the router hop in front of it.
	ExposeInTraffic bool `yaml:"expose_in_traffic"`
}

// GatewayRoute maps a request to a service or stub, matched by exactly one
// of Prefix or Regex. Prefix is a "/"-rooted path prefix; the longest
// matching prefix among a gateway's routes wins, and a trailing "/" on
// Prefix is ignored. With StripPrefix the matched prefix is removed from
// the forwarded path (Prefix routes only).
//
// Regex is a Go regexp matched against the full request path with no
// implicit anchoring — write ^ / $ yourself (e.g. `\.json$` for a
// suffix match). Prefix routes are tried first; Regex routes are only
// considered when no Prefix route matches, in declaration order, first
// match wins.
//
// Rewrite replaces the matched portion of the path instead of just
// removing it — e.g. a /v1 route forwarded as /internal/v1, not just /.
// On a Prefix route it replaces the matched prefix (remainder appended)
// and is mutually exclusive with StripPrefix. On a Regex route it's a
// regexp.ReplaceAllString template (`$1`, `$2`, ...) applied to the whole
// path — only the matched substring changes, the rest is untouched; an
// empty Rewrite leaves a Regex route's path unmodified, as before.
type GatewayRoute struct {
	Prefix      string `yaml:"prefix"`
	Regex       string `yaml:"regex"`
	Service     string `yaml:"service"`
	StripPrefix bool   `yaml:"strip_prefix"`
	Rewrite     string `yaml:"rewrite"`
	// CORSPassthrough, when true, exempts requests matching this route from
	// the gateway's own cors: block entirely — no header injection, no
	// preflight short-circuit, OPTIONS forwarded upstream like any other
	// method. For a route whose backend already emits its own CORS headers
	// (e.g. a framework with CORS middleware built in), sitting behind the
	// same gateway as a route whose backend has none and still needs the
	// gateway's cors:. Inert (no error) on a gateway with no cors: block.
	CORSPassthrough bool `yaml:"cors_passthrough"`
}

// CORSConfig is a gateway's cross-origin resource sharing configuration —
// see core/proxy.CORSPolicy for the matching/response-header semantics
// this compiles down to.
type CORSConfig struct {
	// AllowOrigins lists the origins allowed to read a response. A single
	// "*" entry allows any origin, but only when AllowCredentials is
	// false (validated) — the Fetch spec forbids combining a wildcard
	// origin with credentials.
	AllowOrigins     []string `yaml:"allow_origins"`
	AllowCredentials bool     `yaml:"allow_credentials"`
	AllowMethods     []string `yaml:"allow_methods"`
	AllowHeaders     []string `yaml:"allow_headers"`
	// MaxAgeSeconds, when > 0, sets Access-Control-Max-Age so the
	// browser caches a preflight result instead of repeating it.
	MaxAgeSeconds int `yaml:"max_age_seconds"`
}

// CalledBy resolves the caller-attribution hint for service name: its own
// declared CalledBy when set, else every other service that lists name in
// its own depends_on, sorted — the natural default, since depends_on
// already describes who calls whom for most stacks. Empty when neither
// source names a caller. See Service.CalledBy and core/proxy.Target.CalledBy
// for how the proxy uses this.
func (c *Config) CalledBy(name string) []string {
	if svc, ok := c.Services[name]; ok && len(svc.CalledBy) > 0 {
		out := append([]string(nil), svc.CalledBy...)
		sort.Strings(out)
		return out
	}
	var out []string
	for other, svc := range c.Services {
		if other == name {
			continue
		}
		if slices.Contains(svc.DependsOn, name) {
			out = append(out, other)
		}
	}
	sort.Strings(out)
	return out
}

// RoutablePort resolves the port a gateway route targeting name forwards
// to: a service's intercept (proxy) port when it has one — so the
// gateway -> service hop is captured and per-service latency rules still
// apply — else its real port; a stub's own port. kind is "service" or
// "stub". ok is false for an unknown name, a database, or a service with
// no port at all (a docker service that publishes nothing).
func (c *Config) RoutablePort(name string) (port int, kind string, ok bool) {
	if svc, found := c.Services[name]; found {
		if svc.Proxy > 0 {
			return svc.Proxy, "service", true
		}
		if svc.Port > 0 {
			return svc.Port, "service", true
		}
		return 0, "", false
	}
	if st, found := c.Stubs[name]; found && st.Port > 0 {
		return st.Port, "stub", true
	}
	return 0, "", false
}

// Entity is a dashboard plugin slot: a generic CRUD page over one resource.
type Entity struct {
	Base string `yaml:"base"`
	ID   string `yaml:"id"`
	// Links are optional per-row "open in host app" buttons the Entities
	// tab renders for this entity — see EntityLink.
	Links []EntityLink `yaml:"links,omitempty"`
}

// EntityLink is one "open in host app" button an entity's rows render.
// Template is a plain string with {{column}} placeholders, resolved
// client-side against a row's own fields from the entity's existing rows
// response — no templating engine, no automatic encoding (a template
// embedding one URL inside another query param must be hand
// percent-encoded by whoever writes the config). A placeholder naming a
// column the row doesn't have resolves empty rather than erroring.
type EntityLink struct {
	Label    string `yaml:"label"`
	Template string `yaml:"template"`
}

// Latency holds the config-defined latency injection rules. Defaults are
// applied automatically at `ensemble up` (and reapplied on config
// hot-reload, see orchestrator/reconcile.go) — Profiles are not: each is a
// named, opt-in rule set pulled/applied only via `ensemble latency apply
// <name>`, never on a plain `ensemble up`.
type Latency struct {
	Defaults []LatencyDefault          `yaml:"defaults"`
	Profiles map[string]LatencyProfile `yaml:"profiles"`
}

// LatencyProfile points at a latency profile file — see
// LatencyProfileFile/LoadLatencyProfile. Mirrors Readiness's single-File
// shape.
type LatencyProfile struct {
	// File is the path to the profile's rules file, relative to the
	// directory containing ensemble.yaml (Config.Dir) unless absolute.
	File string `yaml:"file"`
}

// LatencyDefault is one latency rule: fixed delay or a p50/p95/p99 distribution.
type LatencyDefault struct {
	Target  string  `yaml:"target"`
	Path    string  `yaml:"path"`
	FixedMs float64 `yaml:"fixed_ms"`
	P50     float64 `yaml:"p50"`
	P95     float64 `yaml:"p95"`
	P99     float64 `yaml:"p99"`
	Enabled bool    `yaml:"enabled"`
}

// Seed is a named seed target: SQL files and/or HTTP calls to prime a stack.
type Seed struct {
	SQL  []SeedSQL  `yaml:"sql"`
	HTTP []SeedHTTP `yaml:"http"`
}

// SeedSQL loads File (relative to Config.Dir) into Database. TargetDB
// optionally names a specific logical database on Database's server,
// overriding the resource's own default (its POSTGRES_DB/MYSQL_DATABASE)
// — for a shared postgres/mysql container hosting more than one logical
// database, where Database alone can't say which one to seed.
type SeedSQL struct {
	Database string `yaml:"database"`
	File     string `yaml:"file"`
	TargetDB string `yaml:"target_db"`
}

// SeedHTTP issues one HTTP request as part of a seed.
type SeedHTTP struct {
	Method  string            `yaml:"method"`
	URL     string            `yaml:"url"`
	Body    string            `yaml:"body"`
	Headers map[string]string `yaml:"headers"`
}

// Load reads, parses, and validates the ensemble.yaml at path. Dir is set to
// the directory containing path, for resolving relative references (e.g.
// SeedSQL.File) in later stages.
//
// Before parsing, Load expands "${VAR}"/"${VAR:-default}" references
// anywhere in the file (see expandEnvVars) — so ports, images, env values,
// anything, can vary per developer/environment without hand-editing
// ensemble.yaml. A ".env" file next to path is loaded first (see
// loadDotEnv) if one exists, purely as a source of values for that
// expansion; it's entirely optional, and the real process environment
// always takes precedence over it.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}

	dir := filepath.Dir(path)
	dotenv, err := loadDotEnv(filepath.Join(dir, ".env"))
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	data, err = expandEnvVars(data, envLookup(dotenv))
	if err != nil {
		return nil, fmt.Errorf("config: %s: %w", path, err)
	}

	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	c.Dir = dir
	c.dotenv = dotenv

	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// LookupEnv resolves name through the same precedence expandEnvVars uses
// for "${VAR}" references: the real process environment first, then the
// .env file (if any) found next to ensemble.yaml at Load time. Used for
// values that name an environment variable rather than embedding it
// directly — e.g. datadog.api_key_env — so a secret never has to appear in
// ensemble.yaml itself.
func (c *Config) LookupEnv(name string) (string, bool) {
	return envLookup(c.dotenv)(name)
}

// ProfileNames returns every profile the config mentions — each distinct
// Service.Profile value and each top-level Profiles group — sorted.
func (c *Config) ProfileNames() []string {
	seen := map[string]bool{}
	for _, svc := range c.Services {
		if svc.Profile != "" {
			seen[svc.Profile] = true
		}
	}
	for group := range c.Profiles {
		seen[group] = true
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// ProfileMembers returns, sorted, the services that require profile name
// by either mechanism (own Profile field or top-level group listing).
func (c *Config) ProfileMembers(name string) []string {
	seen := map[string]bool{}
	for svcName, svc := range c.Services {
		if svc.Profile == name {
			seen[svcName] = true
		}
	}
	for _, member := range c.Profiles[name] {
		if _, ok := c.Services[member]; ok {
			seen[member] = true
		}
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// ServicesForProfiles returns the services that should be active given the
// set of active profile names.
//
// A service can declare profile membership two ways, and both are honored:
//   - Service.Profile: the service names one profile it belongs to.
//   - the top-level Profiles map: a group name lists member service names
//     (e.g. `profiles: {full: [ledger]}`).
//
// A service's full set of "requirement profiles" is the union of its own
// Profile field (if non-empty) and every top-level group that lists its
// name. Precedence:
//   - Empty requirement set (no own Profile, not listed in any group) ->
//     always included, unchanged from the pre-reconciliation default.
//   - Non-empty requirement set -> included iff it intersects active, i.e.
//     iff AT LEAST ONE of its qualifying profiles is active. So a service
//     is excluded only when *every* mechanism that names it points at an
//     inactive profile; membership in a single active group or an active
//     own-Profile is enough to include it even if another mechanism names
//     an inactive profile.
func (c *Config) ServicesForProfiles(active []string) map[string]Service {
	activeSet := make(map[string]bool, len(active))
	for _, p := range active {
		activeSet[p] = true
	}

	// groupsOf[service] accumulates every top-level Profiles group that
	// lists it, so membership in an active group can be checked below.
	groupsOf := make(map[string][]string)
	for group, members := range c.Profiles {
		for _, name := range members {
			groupsOf[name] = append(groupsOf[name], group)
		}
	}

	out := make(map[string]Service, len(c.Services))
	for name, svc := range c.Services {
		var requires []string
		if svc.Profile != "" {
			requires = append(requires, svc.Profile)
		}
		requires = append(requires, groupsOf[name]...)

		if len(requires) == 0 {
			out[name] = svc
			continue
		}
		for _, p := range requires {
			if activeSet[p] {
				out[name] = svc
				break
			}
		}
	}
	return out
}

// ActivePorts returns every port a stack started with activeProfiles would
// bind, labeled by what claims it ("service catalog", "service catalog
// (proxy)", "database pg", "stub payments", "gateway public" — the same
// labels Validate's duplicate-port check uses). Databases, stubs, and
// gateways have no profile field and always run, so they're always
// included; only services are filtered through ServicesForProfiles. Used
// by `ensemble up`'s preflight port check, so a profile-gated service that
// isn't active right now doesn't make its port's use by something else
// look like a conflict.
func (c *Config) ActivePorts(activeProfiles []string) map[int]string {
	ports := make(map[int]string)
	for name, svc := range c.ServicesForProfiles(activeProfiles) {
		if svc.Port != 0 {
			ports[svc.Port] = "service " + name
		}
		if svc.Proxy != 0 {
			ports[svc.Proxy] = "service " + name + " (proxy)"
		}
	}
	for name, db := range c.Databases {
		if db.Port != 0 {
			ports[db.Port] = "database " + name
		}
	}
	for name, stub := range c.Stubs {
		if stub.Port != 0 {
			ports[stub.Port] = "stub " + name
		}
	}
	for name, gw := range c.Gateways {
		if gw.Port != 0 {
			ports[gw.Port] = "gateway " + name
		}
	}
	return ports
}
