# ensemble

Run your entire backend stack locally — observed. Two products, one core:

- **ensemble** — a topology-agnostic local-stack runner. Point it at your
  services (jars, node, go, containers) with one `ensemble.yaml`; it fronts
  each with a lightweight Go proxy that captures hop-by-hop telemetry (trace
  ids, correlation ids, timings), injects latency on demand, stubs the
  dependencies you can't run locally, and serves a dashboard + REST API.
- **retrace** — record a test run's screenshots and full network hop chain,
  replay blessed recordings as strict mocks in CI (one static binary, no
  stack), and diff runs — pixel, wire, and hop — against references, with a
  PR-style review queue. Retrace the run, see what moved.

Successor to [mezzo](https://github.com/caribou-crew/mezzo): mocks that are
actual recorded dataflow instead of hand-maintained fixtures.

## Status

**Greenfield, in active build. Both `ensemble` and `retrace` run end-to-end.**

| Area | State |
| --- | --- |
| trace model + capturing proxy + stub engine (`core/`) | working |
| orchestrator, REST/SSE API, CLI (`ensemble/`) | working |
| dashboard — topology, traffic, latency, inspector, entities | working |
| `retrace` record/replay/diff/review | working |
| sample stack, test-runner adapters | working |
| published npm / brew packages | not yet released |

Specs live in [`openspec/`](openspec/) — start with
`openspec/changes/init-ensemble-retrace/design.md`. The roadmap and its current
state are in `openspec/changes/init-ensemble-retrace/tasks.md`.

## Try it

Requires Go 1.25 and [pnpm](https://pnpm.io), and Docker only if your config
declares databases.

The dashboard (`dashboard/ensemble-ui`) is embedded into the `ensemble`
binary at compile time, so its JS build has to run **before** the Go build —
otherwise you get a binary that starts fine but serves a "UI not built" page.
`make` handles the ordering:

```sh
git clone https://github.com/caribou-crew/ensemble
cd ensemble
make deps      # pnpm install
make install   # builds the dashboard, then `go install`s ensemble + retrace
```

Make sure `$(go env GOPATH)/bin` is on your `PATH` (add
`export PATH="$PATH:$(go env GOPATH)/bin"` to your shell rc if `ensemble` isn't
found after installing). Once it is, `ensemble` runs from anywhere — no need
to `cd` into the repo or reference a local binary path.

Prefer local binaries instead? `make build` does the same thing but leaves
`./ensemble` and `./retrace` in the repo root rather than installing them.

Without `make`, the equivalent by hand is:

```sh
pnpm install
pnpm -r build                              # must run first — see above
go build -o ensemble ./ensemble/cmd/ensemble
```

If you ever see "UI not built" in the dashboard, it means the Go binary was
built (or reinstalled) without a preceding `pnpm -r build` — rerun `make
install`/`make build`.

Write an `ensemble.yaml` describing your stack:

```yaml
services:
  edge:
    dir: ./services/edge          # working directory for `run`
    run: node server.js           # however this service starts
    port: 8080                    # the port your service listens on
    proxy: 9080                   # ensemble's intercept port in front of it
    health: /healthz              # polled until it answers, before Up returns
    entry: true                   # clients call this one directly
    depends_on: [catalog]

  catalog:
    dir: ./services/catalog
    run: ./gradlew bootRun
    port: 8081
    proxy: 9081
    health: /actuator/health

stubs:                            # dependencies you can't run locally
  payments:
    port: 9099
    routes:
      - match: { method: POST, path: /charges }
        respond:
          status: 201
          headers: { content-type: application/json }
          body: '{"id":"ch_1","status":"succeeded"}'
```

A stub response can also read its body from a file (`body_file:`) instead of
inlining it.

### Profiles as lanes

A stack usually has a couple of verticals, and most days you need one.
Put the optional services in a profile (`profile: lane2` on the service, or
a top-level `profiles: { lane2: [b1, b2] }` group — both work, and a
service named by two profiles stays up while either is active). Services
in no profile are the always-on spine; databases are always on.

```sh
ensemble up                 # spine only (plus whatever --profile names)
ensemble up lane2           # stack already running? lane2 joins it, in dependency order.
                            # nothing running? cold-starts with lane2 active.
ensemble down lane2         # stops lane2's services — and only the ones no other
                            # active lane needs — freeing the memory; proxy ports stay bound
ensemble down               # the whole stack, as before
ensemble profiles           # PROFILE / ACTIVE / SERVICES
```

The same switches are `POST /api/profiles/{name}/up|down` and a toggle
strip on the dashboard's topology view.

### Variants: a stub or the real thing behind one service

Sometimes a service is backed by a 10 MB Go stub that implements the slice
of a monolith you need (still talking to the real databases), and
sometimes by the 1.5 GB monolith itself. `variants:` declares both
backings on the one logical service — same port, proxy, health path, and
dependencies — and `default:` picks what `ensemble up` starts:

```yaml
services:
  monolith:
    port: 8081
    proxy: 9081
    health: /healthz
    depends_on: [postgres]
    default: stub
    variants:
      stub:
        dir: ./services/monolith-stub
        build: go build -o stub .
        run: ./stub
        watch: ["**/*.go"]
      real:
        dir: ../java-monolith
        build: ./gradlew bootJar
        run: java -jar build/libs/app.jar
        env: { JAVA_OPTS: -Xmx1g }
        startup_timeout_s: 120
```

`dir`, `build`, `watch`, `run`, `env`, `docker`, and `startup_timeout_s`
live on the variants; everything else stays on the service. A variant can
just as well be a container — `build:` is any shell command run on your
machine (with your VPN, registry login, and credentials), so building the
image is simply the build step, and `docker:` runs the result:

```yaml
      real:
        dir: ../java-monolith
        build: docker build -t monolith:local .
        watch: ["src/**", "build.gradle", "Dockerfile"]
        startup_timeout_s: 180
        docker:
          image: monolith:local
          ports: ["8081:8080"]            # publish to the service's `port`
          env: { DATABASE_URL: "postgres://…@host.docker.internal:55432/…" }
          args: ["--add-host=host.docker.internal:host-gateway"]   # any extra `docker run` flags
```

Build output streams to `.ensemble/run/<name>.log` as it happens (`tail
-f` it during a long `docker build`), and a failed build's `lastErr` in
`ensemble status` carries the last few KB of that output. `docker.args`
is passed verbatim to `docker run` before the image — `--network`, `-v`,
`--platform`, whatever ensemble has no field for. Switch at
runtime — the stub is killed, the other variant is built if stale and
started, health-gated, and the proxy listener (and any gateway route) never
notices because the port is the service's:

```sh
ensemble variant monolith real          # or the selector on the service's dashboard panel
ensemble up --variant monolith=real     # one-off override of `default`
```

`Restart` keeps the current variant, `ensemble status` shows a VARIANT
column, and build stamps are kept per variant so switching never skips a
stale build.

### Databases

```yaml
databases:
  catalog-db:
    image: postgres:16              # type inferred from the image when omitted
    port: 5432
    seed: ./seed/catalog.sql
    services: [catalog]             # ties this database's health/inspection to catalog
```

`type` is `postgres`, `mysql`, `redis`, `dynamodb`, or `localstack` for a
real emulator ensemble provisions as a container — or `http`, for a service
that keeps its own state outside a database (an in-memory store, a SQLite
file, a wrapped third-party API) and exposes it for inspection itself:

```yaml
databases:
  cardco-go-inspect:
    type: http
    url: http://127.0.0.1:4281/ensemble-inspect
    headers:
      Authorization: "Basic YWRtaW5fY29uc3VtZXI6bWFycWV0YQ=="
    services: [cardco]
```

`url` (required) is the base URL of three GET routes the service itself
implements — `{url}/tables`, `{url}/rows?table=&limit=&offset=`, and
`{url}/fingerprint?table=` — mirroring the same `Tables`/`Rows`/`Fingerprint`
shape every other database driver already exposes; `headers` (optional) are
sent on every request, for a debug surface that needs auth. No container is
provisioned for a `http` database — it just points at a port the service
already owns. Once registered, it behaves exactly like a real database
everywhere else: `GET /api/databases`, the dashboard's schema/rows views,
and the SSE change stream.

local-stack's `cardco-go` (the in-memory Go stand-in for real cardco) is the
reference implementation: it mounts the contract at `/ensemble-inspect/*`,
guarded by the same Basic auth its other routes already require, and serves
`users`/`accounts`/`cards`/`cardProducts`/`digitalWalletTokens`/
`transactions` as JSON round-trips of the structs its own REST API returns.

### Entities

`entities:` gives the dashboard's Entities tab a generic CRUD view over
whatever REST resource a backend already exposes — no per-entity code, just
a base URL and the field that names a row's id:

```yaml
entities:
  users:
    base: http://127.0.0.1:4281/users   # can point through an ensemble intercept port
    id: id                              # defaults to "id" if omitted
```

The Entities tab lists rows as a table (the union of keys actually seen),
and supports viewing/editing/deleting one row and creating new ones —
mutations go straight to `base`, and only show up in Traffic when `base`
itself points at an ensemble intercept port.

`links` (optional) adds one or more "open in host app" buttons to every
row, for jumping from a row straight into whatever tool or app actually
owns that record:

```yaml
entities:
  gadgets:
    base: http://127.0.0.1:4281/gadgets
    id: gadget_id
    links:
      - label: Open in admin-console
        template: "http://localhost:3000/modules?gadgetId={{gadget_id}}"
      - label: Open in Acme Wallet (mobile)
        template: "acmewallet://card?token={{gadget_id}}"
```

`template` is a plain string with `{{column}}` placeholders, resolved
client-side against that row's own fields — a placeholder naming a column
the row doesn't have just resolves to an empty string rather than erroring.
There's no templating engine and no automatic encoding, so a template that
embeds one URL inside another query param needs its inner value hand
percent-encoded. An `http(s)` template opens in a new tab; anything else
(a custom scheme like `acmewallet://`) navigates the current page instead,
since most browsers silently no-op `window.open` on non-http(s) schemes.

A link's `kind: exec` swaps that "navigate" behavior for "copy a local CLI
command to the clipboard" — for reaching a connected Android device or iOS
Simulator, which the browser has no way to open a URL against directly:

```yaml
entities:
  widgets:
    base: http://127.0.0.1:4281/widgets
    id: widget_token
    links:
      - label: Open on iOS Simulator
        kind: exec
        exec: ios-simctl-openurl
        template: "myapp://widget/{{widget_token}}"
      - label: Open on Android
        kind: exec
        exec: adb-view
        template: "myapp://widget/{{widget_token}}"
```

`exec:` names one of a closed, built-in set of commands — currently
`ios-simctl-openurl` (`xcrun simctl openurl booted <url>`) and `adb-view`
(`adb shell am start -a android.intent.action.VIEW -d <url>`). `template:`
resolves the same way as a `kind: url` link's, entirely client-side; the
assembled command (with the resolved URL single-quoted for a safe paste
into your shell) is copied to the clipboard on click, ready to paste into a
terminal and run — ensemble never executes it for you. Hovering the button
shows the exact command that will be copied. A row that's missing a
template column, or whose resolved command would contain a control
character, renders the button disabled with the reason instead of ever
copying something wrong.

This set is **not config-extensible** — you can't point `exec:` at an
arbitrary binary. `ensemble.yaml` is a file that gets committed and shared,
and a free-form command would mean a PR editing it could put a command of
its author's choosing on a teammate's clipboard, one paste away from
running. Adding a new built-in command is a Go change and a code review.

### Gateways

`proxy:` is one-in, one-out — a single intercept port in front of a single
service. Stacks that sit behind an edge router fan one public port out to
many services by path, and a `gateways:` block declares that router without
writing one:

```yaml
gateways:
  public:
    port: 9000                    # the one port your client calls
    routes:
      - { prefix: /products, service: catalog }
      - { prefix: /cart,     service: edge, strip_prefix: true }
      - { prefix: /pay,      service: payments }   # a stub is a valid target
      - { prefix: /,         service: edge }        # optional catch-all
```

Each route forwards onto ensemble's **own resolved port** for the target:
the service's `proxy` port when it has one (so `client → public` and
`public → catalog` are both captured hops, and `latency set --target
catalog` still applies), otherwise its real `port`; a stub forwards to the
stub's `port`. The longest matching prefix wins, matching on path segments
(`/cart` matches `/cart` and `/cart/items`, never `/cartoon`). A request
matching no route gets a `404` and is still recorded as a hop so the
mis-route is visible in `ensemble traffic`. `strip_prefix: true` drops the
matched prefix before forwarding (`/cart/items?limit=5` → `/items?limit=5`).

A `cors:` block makes the gateway add cross-origin response headers and
answer preflight `OPTIONS` requests directly, instead of every backend
needing its own CORS support:

```yaml
gateways:
  public:
    port: 9000
    cors:
      allow_origins: ["http://localhost:3000"]
      allow_methods: ["GET", "POST", "PUT", "DELETE"]
      allow_credentials: false     # a wildcard allow_origins forbids true
    routes:
      - { prefix: /products, service: catalog }
      - { prefix: /cui,      service: cui,     cors_passthrough: true }
```

`cors:` applies to every route on the gateway except one with
`cors_passthrough: true` — for a backend that already emits its own CORS
headers (a framework with CORS middleware built in, say), so the gateway's
own headers don't get added on top and double up (which browsers reject
outright). A passthrough route forwards `OPTIONS` upstream like any other
method rather than answering it directly; `cors_passthrough` on a gateway
with no `cors:` block is accepted but has nothing to pass through, so it's
a no-op.

A gateway is a node like any other: it shows in the topology (as an entry),
`latency set --target public --path /products` delays at the edge, and it
can be the `entry` of a retrace recording session. Gateway names share the
namespace with services, databases, and stubs, and `port` joins the same
collision check as every other port.

Any value in `ensemble.yaml` can reference an environment variable with
`${VAR}` or, with a fallback, `${VAR:-default}` — e.g. `port:
${BFF_PORT:-8003}`, or `dir: ${LOCAL_STACK_DIR:-$HOME/dev/local-stack}` (a
bare `$VAR` inside the `:-default` itself is resolved too, so defaults can
build on other env vars like `$HOME`). `${VAR}` with no default and nothing
set for it is a load error, not a silent empty string. Bare `$VAR` is
recognized only inside a `:-default` fallback, not elsewhere in the file —
a `run:` command's own `$JAVA_HOME`-style references are left alone for the
shell to expand when the service actually starts. A `.env` file next to
`ensemble.yaml` is loaded automatically if present (`KEY=VALUE` per line,
`#` comments, optionally quoted values) as a source of values for that
substitution — it's entirely optional, and a real environment variable
always wins over `.env`.

Then:

```sh
./ensemble up                     # starts everything, serves the dashboard
./ensemble dashboard              # opens it in your browser: topology, live traffic, latency, inspector
./ensemble tui                    # or watch it from the terminal instead: services, traffic, latency, profiles
./ensemble up --tui                # ...or go straight into the terminal UI once the stack is up
./ensemble traffic --follow       # or tail the hops from the terminal, non-interactively
./ensemble down
```

`ensemble tui` (and `ensemble up --tui`) is a terminal client of the same
control-plane API the web dashboard uses — handy over SSH or when you'd
rather not leave the terminal. It covers Services (health, restart, flip
variant, seed), Traffic (a live-scrolling hop feed with a detail pane and
errors-only filter), Latency (arm/disarm/reset rules), and Profiles
(bring profiles up/down); `tab`/`shift+tab` or `1`-`4` switch panels, `q`
quits. It doesn't cover the dashboard's topology graph or database/entity
inspection — those stay browser-only.

Send your app's traffic at the **proxy** ports (`9080` above) rather than the
service ports, and every hop between your services is captured with its trace
id, correlation id, and timings.

### Injecting latency

```sh
./ensemble latency set --target catalog --path / --fixed 400 --enabled
./ensemble latency arm-all --enabled=false      # disarm everything at once
```

Injected delay is reported separately from real upstream time, so a hop's
measured duration stays honest — the true wall-clock is the sum of the two.

Rules persist across `ensemble up` restarts — every `set`/`remove`/`reset`/
`arm-all` (including Datadog pulls, below) is written to
`.ensemble/latency.json` as it happens, and the next `ensemble up` in that
directory restores that exact state instead of reseeding `latency:
defaults:` from `ensemble.yaml`. Defaults only ever seed a store that's
never been persisted before (a first run, or after deleting
`.ensemble/latency.json`); once a state file exists — even an empty one
left by `latency reset` — it's the whole story on every subsequent restart.

### Latency profiles from Datadog

Hand-picking a "realistic" delay means eyeballing a Datadog percentile graph
and typing in roughly what it's been. `ensemble latency from-datadog` and
`ensemble latency apply` do that lookup for you, pulling real p50/p95/p99
numbers straight from Datadog into `LatencyStore`:

```sh
# one ad hoc rule — {P} is substituted with 50/95/99 and queried separately
./ensemble latency from-datadog \
  --target billing --path / \
  --query 'p{P}:trace.http.server.request.duration{service:billing,env:prod}'
# billing /: p50=45ms p95=120ms p99=340ms (source: datadog, last 60m)
```

Credentials come from the environment or `.env` next to `ensemble.yaml` —
never from `ensemble.yaml` itself. The zero-config path needs nothing but
`DD_API_KEY`/`DD_APP_KEY` (and optionally `DD_SITE`) set:

```sh
# .env — gitignored, same as any other secret file
DD_API_KEY=...
DD_APP_KEY=...
```

An optional top-level `datadog:` block customizes the site and *which* env
vars carry the keys (never the key values themselves), plus a default query
window and a service-name mapping for when ensemble's name and Datadog's
service tag differ:

```yaml
datadog:
  site: datadoghq.com              # default
  api_key_env: DD_API_KEY          # default
  app_key_env: DD_APP_KEY          # default
  default_window_minutes: 60       # default
  service_map:
    statements: accounts-statements
```

A **latency profile** is a named, file-backed set of rules — mix Datadog
pulls and plain fixed delays — applied together with one command:

```yaml
latency:
  profiles:
    production:
      file: tools/latency-production.yaml   # relative to ensemble.yaml
```

```yaml
# tools/latency-production.yaml
rules:
  - target: billing
    path: /
    from_datadog:
      query: "p{P}:trace.http.server.request.duration{service:billing,env:prod}"
      window_minutes: 60           # optional, falls back to datadog.default_window_minutes
  - target: statements
    path: /v3/statements
    fixed_ms: 25
```

```sh
./ensemble latency apply production
# billing /: p50=45ms p95=120ms p99=340ms (source: datadog)
# statements /v3/statements: fixed=25ms
# 2 applied, 0 failed
```

Latency profiles are strictly **opt-in** — a plain `ensemble up` never reads
or applies one; `apply` is a separate, explicit step. Applying is
best-effort per rule: one rule's Datadog error (no data in the window, a bad
query, an auth failure) is reported against that rule and never blocks the
rest of the profile. `latency list` shows a pulled rule's `source` (`manual`
for anything hand-set with `latency set`) so a suspicious number is always
traceable back to the query and window it came from. Pulled rules are
stored **disarmed** — arm them explicitly with `--enabled` or `latency
arm-all --enabled`.

Note the naming proximity to, but non-collision with, the top-level
`profiles:` key (service activation lanes — see "Profiles as lanes" above):
`latency.profiles` is a different concept at a different YAML path. Docs and
`--help` say "latency profile" in full for exactly this reason.

### Preflight checks

Some failures have nothing to do with ensemble.yaml — docker/podman isn't
running, a VPN is down, an internal service the stack depends on is
unreachable — and surfacing them deep inside the first `docker run` or build
step is confusing. A top-level `preflight:` key runs commands up front and
fails `ensemble up` fast, before anything starts:

```yaml
preflight:
  - name: container runtime
    run: podman info
    message: "podman isn't running — start it and try again"
  - name: vpn
    run: curl -sf https://internal.example.com/health
    timeout_s: 5   # default 10
```

Each check runs `run` under `/bin/sh -c`; a non-zero exit fails the whole
command with `message` (if set) or the command's own output. Checks run in
order and stop at the first failure — nothing is started, no port is bound,
until every check passes.

### Readiness checks

`health:` proves a process is listening; `on_ready` proves seeds/migrations ran.
Neither proves the seeded data is actually queryable end-to-end — a stack can
report every service `healthy` while being unusable (wrong schema, a silently
failed seed, misconfigured auth). A top-level `readiness:` key closes that gap:

```yaml
readiness:
  file: tools/readiness.yaml   # relative to ensemble.yaml
  timeout_s: 30                # total retry budget (default 60)
  retry_interval_s: 2           # delay between retries of a still-failing check (default 5)
```

```yaml
# tools/readiness.yaml
checks:
  - name: catalog-healthy
    service: catalog             # resolved the same way a gateway route resolves a service
    path: /healthz
    assert:
      status: 200

  - name: statements-seeded
    service: statements
    path: /v3/statements/fa5e4374-0000-4000-8000-000000004374
    headers_from: ./readiness-auth.sh   # a script; its stdout lines become request headers
    assert:
      status: 200
      body_jq: ".data | length > 0"     # evaluated against the JSON response body
```

Checks run once, after `on_ready` completes, retried until every check has
passed or `timeout_s` elapses — a check that already passed is never
re-executed. This runs in the background, so it never delays `ensemble up`
returning; `ensemble ready` (below) is the thing that blocks on it, and
`ensemble status` reports a summary (`READINESS: 2/2 passed`) alongside the
per-service table. See `sample/tools/readiness.yaml` for a complete example,
including the `headers_from` auth pattern.

### CLI

```
ensemble up [-c ensemble.yaml] [--profile p1,p2] [--api 127.0.0.1:4700] [--tui]
ensemble dashboard [--no-open]
ensemble tui
ensemble status | ready [--timeout DURATION] | down | seed <name>
ensemble latency list | set | reset | arm-all | from-datadog | apply <profile>
ensemble traffic [--since N] [--errors-only] [--follow]
ensemble trace <traceId> [--export har|curl|raw]
```

`ensemble ready` blocks until the stack's readiness checks resolve (or
`--timeout` elapses), exiting 0/1 — the deterministic gate for CI:
`ensemble up && ensemble ready && pnpm test:e2e`.

Every command takes `--json`, and every one is a thin client over the REST API,
so anything the CLI does an agent or script can do over HTTP. `ENSEMBLE_API`
sets the default endpoint. The control plane binds loopback only.

## Layout

| Path | What |
| --- | --- |
| `core/` | Go shared module: trace model, proxy, stub engine |
| `ensemble/` | the runner: orchestrator, REST/SSE server, inspector, CLI |
| `retrace/` | record/replay/diff: engines, review server, CLI (not yet built) |
| `dashboard/` | React UIs, embedded into the binaries |
| `openspec/` | specs and roadmap |

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).
