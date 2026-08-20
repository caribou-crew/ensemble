# Phase 2: ensemble runner (orchestrator + API + inspector + CLI) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the `ensemble` product: read `ensemble.yaml`, run/supervise the user's services and databases, front them with the Phase 1 proxy, and expose everything over REST/SSE + a CLI.

**Architecture:** `ensemble/` Go module consuming `core/` (module `github.com/ensemble-dev/ensemble/core`, already implemented and tested). One process: orchestrator supervises children, proxy listeners intercept, HTTP server serves `/api/*`. CLI is a thin REST client.

**Tech Stack:** Go 1.25 stdlib-first. Allowed third-party deps (each justified): `gopkg.in/yaml.v3` (config), `github.com/mysql & pgx` drivers and dynamo SDK ONLY in task 2.5 as listed there. NO web framework — `net/http.ServeMux` (Go 1.22+ patterns: `mux.HandleFunc("GET /api/status", ...)`).

**Spec:** `openspec/changes/init-ensemble-encore/design.md` §4 (runtime architecture, config contract), `specs/ensemble-orchestrator/spec.md`, `specs/ensemble-proxy/spec.md`, `specs/ensemble-api-dashboard/spec.md`. The spec is the binding authority.

## Global Constraints

- Go stdlib-first; every new third-party dep must be named in this plan or the commit body explains why.
- One hop schema: `core/trace` types only — never redefine hop/trace shapes.
- API-first parity: every capability is a REST/SSE JSON endpoint first; CLI consumes the API.
- Redaction at capture (already in core); the server never adds secrets to responses; DB DSNs/creds never appear in `/api/*` output.
- `go test -race ./core/... ./ensemble/... ./encore/...` green at every commit;
  same pattern for `go vet` (bare `./...` cannot work from the workspace root —
  it is not a module directory).
- Repo root is a Go workspace (`go.work`) — run commands from repo root; the ensemble module lives at `ensemble/`.
- Commit after each task with a conventional message; never commit unrelated files.

## Phase 1 interfaces you consume (already built, in `core/`)

```go
// core/trace
trace.Hop{Schema, Seq, TraceID, SpanID, ParentSpanID, CorrelationID, Session,
          From, To, Method, Path, Status, T Timings{Start, FirstByteMs, DoneMs},
          Req/Resp Payload{Headers map[string]string, Body string, Truncated bool},
          InjectedDelayMs, Err}
trace.NewWriter(io.Writer).Write(Hop) / trace.NewReader(io.Reader).Next() (Hop, error) // ErrEOF
trace.NewRedactor(userKeys []string, maxBody int) *Redactor
trace.CollapseRelays([]Hop, enabled bool) []LogicalHop ; trace.MergeForDetail(LogicalHop) Hop
trace.ToHar([]Hop) Har ; trace.ToCurl(Hop) string ; trace.ToRawRequest/ToRawResponse(Hop) string
trace.Verdict (ok/suspect/degraded/broken/failed), v.Worse(o)

// core/proxy
proxy.NewRecorder(proxy.RecorderOpts{Ring int, Redactor *trace.Redactor, Writer *trace.Writer}) *Recorder
rec.Record(Hop) Hop ; rec.Snapshot() []Hop ; rec.Subscribe(cursor uint64) (<-chan trace.Hop, func())
p := proxy.New(rec) ; p.Latency = proxy.NewLatencyStore(nil)
p.Serve(proxy.Target{Name, Listen, Upstream, InjectBaggage}) (addr string, err error)
p.ServeStoppable(t) (addr string, stop func(), err error) ; p.Close()
proxy.LatencyRule{Target, Path, FixedMs, P50, P95, P99, Enabled} // JSON tags exist
lat.Set(rule) / Remove(target, path) / Rules() / Reset() / ArmAll(bool) / DelayFor(target, path)
proxy.NewSessionManager(p, rec, entryTargets []string) *SessionManager
mgr.Start(id, entryName, entryUpstream) (*Session{ID, EdgeAddr, Hops(), Verdict()}, error)
mgr.End(id) *Session ; mgr.Close()

// core/stub
stub.New(name string, routes []stub.Route, rec *proxy.Recorder) *Stub
s.Serve(listen) (addr, error) ; s.Close()
stub.Route{Match{Method, Path}, Respond{Status, Headers, Body, BodyFile, Template}}
```

---

### Task 2.1: Config — `ensemble.yaml` schema, validation, defaults

**Files:**
- Create: `ensemble/config/config.go`, `ensemble/config/validate.go`
- Test: `ensemble/config/config_test.go` (+ `testdata/*.yaml` fixtures)

**Interfaces:**
- Consumes: nothing new. Dep: `gopkg.in/yaml.v3` (allowed).
- Produces (later tasks rely on these exact names):

```go
package config

type Config struct {
    Services  map[string]Service  `yaml:"services"`
    Databases map[string]Database `yaml:"databases"`
    Stubs     map[string]Stub     `yaml:"stubs"`
    Entities  map[string]Entity   `yaml:"entities"`
    Latency   Latency             `yaml:"latency"`
    Seeds     map[string]Seed     `yaml:"seeds"`
    Profiles  map[string][]string `yaml:"profiles"`
    Redact    []string            `yaml:"redact"`   // extra redaction keys
    Dir       string              `yaml:"-"`        // dir containing the config file (set by Load)
}
type Service struct {
    Dir       string            `yaml:"dir"`
    Build     string            `yaml:"build"`
    Watch     []string          `yaml:"watch"`      // globs for build freshness
    Run       string            `yaml:"run"`
    Port      int               `yaml:"port"`       // real service port
    Proxy     int               `yaml:"proxy"`      // intercept port (0 = auto-assign later)
    Env       map[string]string `yaml:"env"`
    Health    string            `yaml:"health"`     // path, e.g. /healthz
    DependsOn []string          `yaml:"depends_on"`
    Docker    *DockerPlacement  `yaml:"docker"`
    Entry     bool              `yaml:"entry"`      // clients call this directly
    Profile   string            `yaml:"profile"`    // "" = always on
}
type DockerPlacement struct { Image string `yaml:"image"`; Ports []string `yaml:"ports"`; Env map[string]string `yaml:"env"` }
type Database struct { Image string; Port int; Type string; Seed string; Env map[string]string; Services []string } // yaml tags lowercase; Type: postgres|mysql|redis|dynamodb|localstack (default from image name)
type Stub struct { Port int `yaml:"port"`; Routes []StubRoute `yaml:"routes"` }
type StubRoute struct{ Match StubMatch `yaml:"match"`; Respond StubRespond `yaml:"respond"` }
type StubMatch struct{ Method, Path string }           // lowercase yaml tags
type StubRespond struct{ Status int; Headers map[string]string; Body, BodyFile string `yaml:"body_file"`; Template bool }
type Entity struct{ Base string `yaml:"base"`; ID string `yaml:"id"` }
type Latency struct{ Defaults []LatencyDefault `yaml:"defaults"` }
type LatencyDefault struct{ Target, Path string; FixedMs, P50, P95, P99 float64; Enabled bool }
type Seed struct{ SQL []SeedSQL `yaml:"sql"`; HTTP []SeedHTTP `yaml:"http"` }
type SeedSQL struct{ Database, File string }            // file relative to Config.Dir
type SeedHTTP struct{ Method, URL, Body string; Headers map[string]string }

func Load(path string) (*Config, error)                 // read + parse + Validate + set Dir
func (c *Config) Validate() error                       // all violations joined via errors.Join
func (c *Config) ServicesForProfiles(active []string) map[string]Service // filters by Profile
```

**Validation rules (each is a test case):**
1. service with neither `run` nor `docker` → error naming the service
2. service with `run` but `port: 0` → error
3. duplicate proxy port across services/stubs → error listing both names
4. `depends_on` referencing an unknown service/database → error
5. database `type` not in the allowed set (after defaulting from image: image containing "postgres"→postgres, "mysql"→mysql, "redis"→redis, "dynamodb"→dynamodb, "localstack"→localstack) → error
6. stub route with empty `match.path` → error
7. seed referencing unknown database → error
8. entity with empty `base` → error
9. valid full config (mirror design.md §4.3 example incl. dynamodb + localstack + profiles) → parses, defaults applied, `ServicesForProfiles(nil)` excludes profiled services, `ServicesForProfiles([]string{"full"})` includes them

**Steps:**
- [ ] Write `config_test.go` table tests + `testdata/valid-full.yaml`, run: FAIL (types undefined)
- [ ] Implement types + Load + Validate + ServicesForProfiles
- [ ] `cd /Users/steven/dev/oss/ensemble && go test -race ./ensemble/config/` PASS; `go vet ./ensemble/...`
- [ ] Commit: `feat(ensemble/config): ensemble.yaml schema, validation, profiles`

### Task 2.2: Orchestrator — process supervisor + Docker driver

**Files:**
- Create: `ensemble/orchestrator/proc.go` (native process), `ensemble/orchestrator/docker.go` (docker CLI driver), `ensemble/orchestrator/orchestrator.go` (dependency ordering + health gates + wiring), `ensemble/orchestrator/health.go`
- Test: `ensemble/orchestrator/orchestrator_test.go`, `proc_test.go`

**Interfaces:**
- Consumes: `config.Config/Service/Database` from Task 2.1; `core/proxy` for intercept wiring.
- Produces:

```go
package orchestrator

type Status string // "stopped","building","starting","healthy","unhealthy","failed"
type ServiceState struct {
    Name string; Status Status; Placement string /* "native"|"docker" */
    PID int; ProxyPort int; Port int; StartedAt time.Time; LastErr string
}
type Orchestrator struct{ /* ... */ }
func New(cfg *config.Config, px *proxy.Proxy, opts Opts) *Orchestrator
type Opts struct{ Profiles []string; Logf func(string, ...any); LogDir string }
func (o *Orchestrator) Up(ctx context.Context) error     // deps-ordered start, health-gated
func (o *Orchestrator) Down() error
func (o *Orchestrator) Restart(ctx context.Context, name string) error // re-runs build if stale
func (o *Orchestrator) States() []ServiceState            // sorted by name
func (o *Orchestrator) Service(name string) (ServiceState, bool)
```

**Behavior requirements:**
- Native start: `exec.Command("/bin/sh", "-c", svc.Run)` with `Dir: resolved(svc.Dir)` (relative to `cfg.Dir`, `~` expanded), env = `os.Environ()` + `svc.Env`. Process group kill on Down (`Setpgid`, kill negative pid) so shell children die.
- Service stdout/stderr → `<LogDir>/<name>.log` (append, created if missing).
- Build-if-stale: run `svc.Build` when any file matching `svc.Watch` globs is newer than the newest file matching... simpler and testable: store the build's completion time in `<LogDir>/<name>.buildstamp`; rebuild when any watched file's mtime > stamp (or stamp missing and Build != ""). No watch globs + Build set → always build.
- Health gate: if `svc.Health != ""`, poll `http://127.0.0.1:<port><health>` every 250ms until 2xx or 30s timeout (timeout → Status failed, Up returns error naming the service). No health path → consider healthy once the process runs (native) or container is running (docker) plus, when Port > 0, a successful TCP dial.
- Dependency order: topological sort over `depends_on` (services + databases); cycle → error naming the cycle. Databases start before dependents; parallel start within a level is fine but not required.
- Docker driver: shell out to `docker` CLI (no SDK dep): `docker run -d --name ensemble-<name> -p <ports> -e K=V <image>`, `docker rm -f` on Down, `docker inspect -f '{{.State.Running}}'` for status. Container names always prefixed `ensemble-`. Databases run via this driver; a Service with `docker` placement too.
- Proxy wiring: for every active service with `Proxy > 0`, call `px.Serve(proxy.Target{Name: name, Listen: fmt.Sprintf("127.0.0.1:%d", svc.Proxy), Upstream: "http://127.0.0.1:" + port})` during Up.
- Docker unavailable (CI without docker): if config has no databases and no docker services, Up must work without docker installed. Gate docker probing on need.

**Tests (no docker in CI — use fake commands):**
1. Topological order: config a→b→c (depends_on), Up records start order c,b,a. Use `run: /bin/sh -c 'sleep 30'` services with no health (TCP gate via prestarted listeners is overkill — set Port 0) and assert via States()/hook. Provide an internal test hook: `o.testStartHook func(name string)` (unexported, set from test).
2. Cycle detection: a↔b → Up error mentions both.
3. Health gate failure: service with health path but nothing listening → Up returns error, state failed, within timeout override (make timeout a field in Opts: `HealthTimeout time.Duration`, default 30s, test uses 500ms).
4. Real supervision: start `run: /bin/sh -c 'while true; do sleep 1; done'`, assert PID alive, Down kills the process group (poll `syscall.Kill(pid, 0)` fails).
5. Build-if-stale: temp dir with `watch: ["*.txt"]`, build command `touch built`; first Restart builds, second (no changes) skips, touching a .txt file re-builds. Assert via mtimes/existence.
6. Proxy wiring: service with Proxy port set, upstream = httptest server; Up; GET through the intercept port returns upstream body and a hop lands in the recorder.

**Steps:**
- [ ] Write tests, run FAIL → implement → `go test -race ./ensemble/orchestrator/` PASS → vet
- [ ] Commit: `feat(ensemble/orchestrator): supervised native+docker services, health gates, dependency order, build-if-stale`

### Task 2.3: Placement flip + seeds executor

**Files:**
- Modify: `ensemble/orchestrator/orchestrator.go` (+`flip.go`)
- Create: `ensemble/orchestrator/seed.go`
- Test: `ensemble/orchestrator/flip_test.go`, `seed_test.go`

**Interfaces:**
- Produces:
```go
func (o *Orchestrator) Flip(ctx context.Context, name string) error // native<->docker; error if service lacks the other placement
func (o *Orchestrator) Seed(ctx context.Context, name string) ([]SeedStepResult, error)
type SeedStepResult struct{ Kind string /*sql|http*/; Ref string; OK bool; Err string; DurationMs float64 }
```
- SQL seed execution: task 2.5 owns DB drivers; here define the seam
  `type SQLRunner interface { RunFile(ctx, dbName, path string) error }` field on Orchestrator (`o.SQLRunner`), settable; seed SQL steps error cleanly if nil ("no SQL runner configured"). HTTP steps: plain `http.Client` with 10s timeout, 2xx = OK.

**Behavior:**
- Flip stops the current placement (process kill / docker rm), starts the other, keeps ProxyPort identical (proxy listener untouched — it points at 127.0.0.1:port which stays the contract; docker placement must publish the same host port).
- Flip on a service with only one placement → error "service X has no alternate placement".
- Seed runs steps in declared order, stops at first failure, returns results for executed steps (spec: report per-step results).

**Tests:** flip native→docker with a fake docker binary on PATH (test writes a shell script into t.TempDir, prepends to PATH) recording invocations; seed http steps against httptest servers (success + failure ordering); seed sql without runner errors.

**Steps:** tests FAIL → implement → PASS → vet → commit `feat(ensemble/orchestrator): live placement flip and named seed executor`

### Task 2.4: API server — REST + SSE

**Files:**
- Create: `ensemble/server/server.go`, `ensemble/server/routes.go`, `ensemble/server/sse.go`, `ensemble/server/openapi.go`
- Test: `ensemble/server/server_test.go`

**Interfaces:**
- Consumes: orchestrator (Task 2.2/2.3 API), `core/proxy` (Recorder, LatencyStore, SessionManager), `core/trace` (exports, collapse), config.
- Produces:
```go
package server
type Deps struct {
    Cfg *config.Config; Orch *orchestrator.Orchestrator
    Rec *proxy.Recorder; Lat *proxy.LatencyStore; Sessions *proxy.SessionManager
    Version string
}
func New(d Deps) http.Handler          // all /api routes; later embeds dashboard at /
func Serve(ctx context.Context, addr string, h http.Handler) error
```

**Endpoints (all JSON; errors as `{"error":"..."}` with 4xx/5xx):**
- `GET /api/health` → `{ok:true, version}`
- `GET /api/status` → orchestrator states + proxy ports
- `GET /api/topology` → nodes (services/databases/stubs with category, status, entry flag) + edges from `depends_on` and env-wired proxy references (an env value containing `127.0.0.1:<proxyPort>` of another service = an edge)
- `POST /api/services/{name}/restart`, `POST /api/services/{name}/flip`
- `POST /api/seed/{name}` → SeedStepResult list
- `GET /api/traffic?since=<seq>&limit=<n>&errorsOnly=&session=` → hops from Recorder.Snapshot filtered
- `GET /api/traffic/stream?since=<seq>` → SSE (`event: hop`, `data: <hop json>`, heartbeat comment every 15s, honors client disconnect)
- `GET /api/traces/{traceId}` → hops of that trace + collapsed logical view (`trace.CollapseRelays(hops, true)`)
- `GET /api/traces/{traceId}/export?format=har|curl|raw` → the export
- `GET /api/latency` / `PUT /api/latency` (upsert rule) / `DELETE /api/latency?target=&path=` / `POST /api/latency/arm-all {enabled}` / `POST /api/latency/reset`
- `POST /api/sessions {id, entry}` → starts session via SessionManager (entry service must exist and have a proxy port; upstream = its intercept addr), returns `{id, edgeAddr}`
- `DELETE /api/sessions/{id}` → ends, returns `{id, hops: <count>, verdict, reasons}`
- `GET /api/sessions/{id}/hops` → NDJSON stream of session hops
- `GET /api/openapi.json` → hand-written minimal OpenAPI 3.1 doc listing the above (paths, methods, summaries — no full schemas required)
- Every mutating endpoint also records an annotation into the Recorder as a hop with `To: "ensemble-control"`, Method = the HTTP method, Path = the endpoint, Status = response code (this is the "mutations logged as annotation events" requirement).

**Tests:** httptest.NewServer(New(deps)) with a real orchestrator on a tiny fake config (one httptest-backed service) — status/topology shapes; latency CRUD round-trip drives a real LatencyStore; traffic returns recorded hops; SSE test reads 2 events then disconnects; sessions start/end round-trip with a real SessionManager over a proxied httptest upstream; export returns HAR with entries; control-plane annotation hop appears after a PUT /api/latency.

**Steps:** tests FAIL → implement → PASS (`-race`) → vet → commit `feat(ensemble/server): REST+SSE control plane with OpenAPI listing and control annotations`

### Task 2.5: Inspector — postgres, mysql, dynamodb drivers

**Files:**
- Create: `ensemble/inspector/inspector.go` (interface + registry + change-stream engine), `ensemble/inspector/postgres.go`, `ensemble/inspector/mysql.go`, `ensemble/inspector/dynamo.go`, `ensemble/inspector/sqlrunner.go` (implements orchestrator.SQLRunner)
- Test: `ensemble/inspector/inspector_test.go` (change-stream engine against a fake driver), `postgres_test.go`/`mysql_test.go`/`dynamo_test.go` (integration, guarded: `t.Skip` unless `ENSEMBLE_TEST_PG_DSN`/`..._MYSQL_DSN`/`..._DYNAMO_ENDPOINT` env set)

**Allowed deps:** `github.com/jackc/pgx/v5/stdlib`, `github.com/go-sql-driver/mysql`, and for dynamo use plain HTTP against DynamoDB Local's JSON API (`X-Amz-Target: DynamoDB_20120810.ListTables` etc. with dummy creds — NO aws-sdk-go-v2, it's enormous; document this in the commit).

**Interfaces:**
```go
package inspector
type Driver interface {
    Tables(ctx) ([]Table, error)                       // Table{Name string, Columns []Column{Name,Type,Nullable}}
    Rows(ctx, table string, limit, offset int) ([]map[string]any, error)
    Fingerprint(ctx, table string) (string, error)     // cheap change token: max(pk)+count or stream shard seq
}
type Inspector struct{ /* registry name->Driver + poller */ }
func New() *Inspector ; func (i *Inspector) Register(name string, d Driver)
func (i *Inspector) Schema(ctx, db string) ([]Table, error)
func (i *Inspector) Rows(ctx, db, table string, limit, offset int) ([]map[string]any, error)
func (i *Inspector) Watch(interval time.Duration) (<-chan ChangeEvent, func()) // poller: fingerprint diff -> ChangeEvent{DB, Table, At}
```
- Change stream tier 1 (snapshot diff around GUI mutations) is deferred to the entity-pages task in Phase 3 — this task ships the poller tier. Note that in the commit body.

**Tests:** engine test with a scripted fake driver (fingerprint changes on demand → events fire, dedup within same tick); driver integration tests behind env guards; sqlrunner executes a file of `;`-separated statements in order (integration-guarded for pg/mysql, plus a unit test of statement splitting incl. quoted semicolons).

**Steps:** tests FAIL → implement → PASS → vet → commit `feat(ensemble/inspector): pg/mysql/dynamo drivers, poll-based change stream, seed SQL runner`

### Task 2.6: CLI — thin REST client + `--json`

**Files:**
- Create: `ensemble/cmd/ensemble/main.go` (replace stub), `ensemble/cmd/ensemble/client.go`, `ensemble/cmd/ensemble/cmd_*.go`
- Test: `ensemble/cmd/ensemble/cli_test.go`

**Scope ruling:** the full Ink-style TUI cockpit is Phase 3-adjacent; THIS task ships the flag-based CLI only (spec's cockpit requirement is satisfied later; note in commit). No bubbletea dep yet.

**Commands (all support `--json`; default output human tables via text/tabwriter):**
- `ensemble up [-c ensemble.yaml] [--profile full] [--api :4700]` — loads config, builds Recorder(+NDJSON writer under `.ensemble/hops.jsonl`)+Proxy+LatencyStore(+config defaults)+SessionManager(entry targets from config)+Orchestrator+stubs, serves API, blocks until SIGINT → Down. Redactor keys from `cfg.Redact`.
- `ensemble status` / `ensemble down` (down = POST not needed: status prints; down sends SIGINT equivalent via `POST /api/shutdown` — add that endpoint here in server, guarded to loopback)
- `ensemble seed <name>`
- `ensemble latency list|set --target X --path / --fixed 100 [--enabled]|reset|arm-all --enabled=true`
- `ensemble traffic [--since N] [--errors-only] [--follow]` (follow = SSE)
- `ensemble trace <traceId> [--export har|curl|raw]`
- `--api-url` flag / `ENSEMBLE_API` env for client commands (default `http://127.0.0.1:4700`).

**Tests:** run `up` in-process (call its run function, not exec) against a config with one httptest-backed service on a free port; then drive `status --json`, `latency set` + `list --json`, `traffic --json` through the client functions and assert JSON shapes; SIGINT path via context cancel.

**Steps:** tests FAIL → implement → PASS → vet → commit `feat(ensemble/cmd): ensemble CLI — up/status/seed/latency/traffic/trace as REST client`

---

## Verification at phase end

From repo root: `go test -race ./... && go vet ./...` all green; `ensemble up` against a hand-rolled two-service demo config manually smoke-tested (controller does this, not a subagent).
