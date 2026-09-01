<p align="center">
  <img src="docs/images/logo.svg" alt="ensemble" height="64">
</p>

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
| published npm packages | released — `@caribou-crew/ensemble`, `@caribou-crew/retrace` |
| brew package | not yet released |

Specs live in [`openspec/`](openspec/) — start with
`openspec/changes/init-ensemble-retrace/design.md`. The roadmap and its current
state are in `openspec/changes/init-ensemble-retrace/tasks.md`.

## Dashboard

`ensemble up` serves a live dashboard — topology, traffic, latency injection,
a DB inspector, and CRUD over your seeded entities, all reading the same
captured trace data. Screenshots below are from the [sample "brew"
stack](sample/) mid-checkout.

**Topology** — the stack's services, databases, and stubs, grouped by role,
with live health and traffic-hot highlighting.

![Topology view](docs/images/dashboard/topology.png)

**Traffic** — every captured hop across the stack, in order, with the
injected-latency annotation visible on `catalog` requests.

![Traffic view](docs/images/dashboard/traffic.png)

**Latency** — arm per-target delay rules (fixed or percentile-shaped) without
touching service code.

![Latency view](docs/images/dashboard/latency.png)

**Inspector** — browse the databases behind your services directly.

![Inspector view](docs/images/dashboard/inspector.png)

**Entities** — CRUD over rows `entities:` maps in `ensemble.yaml`, without a
DB client.

![Entities view](docs/images/dashboard/entities.png)

**Services** — start/stop/restart, flip native ↔ docker, or swap variants,
per service.

![Services view](docs/images/dashboard/services.png)

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
`bin/ensemble` and `bin/retrace` in the repo rather than installing them
(they can't sit at the repo root itself — `ensemble/` and `retrace/` are
the module directories).

Without `make`, the equivalent by hand is:

```sh
pnpm install
pnpm -r build                              # must run first — see above
go build -o bin/ensemble ./ensemble/cmd/ensemble
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

### Passthrough mode: flipping a service to a real remote environment

`upstream:` makes a service a passthrough target: instead of spawning
`run`/`docker`, the proxy forwards straight to a real remote base URL —
QA, staging, whatever environment you'd otherwise hit through a hand-rolled
client with none of ensemble's capture/redaction/diff machinery. A service
can declare `upstream:` **alongside** `run`/`docker`, which makes it
flippable between its local placement and passthrough live, exactly like
[variants](#variants-a-stub-or-the-real-thing-behind-one-service) — or
`upstream:` alone, for a service with no local backing at all (it just
can't be flipped back to anything):

```yaml
services:
  ops:
    dir: ./services/ops-bff
    run: node index.js
    proxy: 9086
    upstream: "https://ops-qa.internal.example.com"
    passthrough: qa
```

`passthrough:` is a label for the remote environment (any non-empty
string — it's never matched against anything, not even `"prod"`) and is
what arms the safety rail: a passthrough target refuses any non-GET/HEAD
request with a `502`, recorded as a hop rather than silently dropped,
unless the service also sets `allow_writes: true`. Fault/latency injection
rules are skipped against a passthrough target by default too, so a
stack-wide rule can't accidentally reach a real remote.

Flip a flippable service from the dashboard's Services tab (a button for
two declared placements, a target-picking select once a service declares
three) or the REST endpoint directly:

```sh
curl -X POST localhost:8080/api/services/ops/flip -d '{"target":"passthrough"}'
curl -X POST localhost:8080/api/services/ops/flip -d '{"target":"native"}'
```

A body-less `POST .../flip` keeps the legacy binary native/docker toggle,
so existing callers (including the TUI's `f` key) are unaffected.

For a remote that requires mTLS, add a client certificate — the key
itself is never written to config, only the name of an env var holding it:

```yaml
    client_cert_file: ./certs/ops-qa-client.pem   # relative to ensemble.yaml
    client_key_env: OPS_QA_CLIENT_KEY             # PEM key, from .env or the shell
    client_key_passphrase_env: OPS_QA_CLIENT_KEY_PASSPHRASE   # optional
```

A recording that touched a passthrough leaf discloses it rather than
silently narrowing: `retrace diff --json` includes `reducedScope`, the
services (from either side) that were passthrough targets during capture
— so a diff involving one is legible about which part of the chain wasn't
actually witnessed, instead of a human having to already know.

One caveat: `retrace revalidate` replays a recorded request's headers
verbatim except any value the recording redacted (`Authorization`, DPoP)
— which it always does for a passthrough target's real credential, same as
any other hop. Revalidating against an auth-gated passthrough target will
therefore report the resulting `401` as drift; that's `revalidate`'s
existing redaction behavior, not something specific to passthrough. Live
credential resolution for `revalidate` is future work.

### Gateway passthrough: flipping a gateway to a real remote edge

Service-level passthrough (above) flips one service at a time. A
`Gateway` fans requests out to many services/stubs by its own route
table, and sometimes what you actually want is simpler: point the
*whole* gateway at a real remote edge — QA's own envoy, say — and let it
own the routing, exactly as if the client had pointed there directly.
`upstreams:` declares any number of tagged remote targets a gateway can
be flipped to:

```yaml
gateways:
  public:
    port: 9100
    routes:
      - { prefix: /bff, service: storefront, strip_prefix: true }
    upstreams:
      - name: qa
        url: https://qa.example.com
        allow_writes: false
```

Flipping `public` to `qa` makes it forward every request verbatim to
`https://qa.example.com` — no `routes:` matching or rewriting, no local
CORS handling, no fault/latency injection. It's a pure pass-through, not
a second router layered on top of the real one; capturing/diffing this
traffic reflects exactly what the remote edge does, nothing ensemble
added. Flipping back to `local` restores the gateway's configured routing
exactly as before. Like service passthrough, this is a runtime action,
not a config edit — it resets to `local` on the next `ensemble up`.

The same read-only-by-default safety rail and mTLS fields
(`allow_writes`, `client_cert_file`, `client_key_env`,
`client_key_passphrase_env`) apply per upstream, identical in meaning to
their service-level counterparts above.

Flip from the dashboard's Services tab (gateways get the same flip
control services do, offering `local` plus every declared upstream) or
the REST endpoint directly — `target` is always required, since a
gateway has no binary toggle to fall back to:

```sh
curl -X POST localhost:8080/api/gateways/public/flip -d '{"target":"qa"}'
curl -X POST localhost:8080/api/gateways/public/flip -d '{"target":"local"}'
```

`retrace diff`'s `reducedScope` disclosure (above) covers a
passthrough-flipped gateway the same way it already covers a passthrough
service — a run that went through one is honestly marked reduced-scope,
not silently presented as a fully witnessed chain.

Not built yet: flipping several gateways together under one named
environment — today each gateway flips independently.

### Caller attribution: `called_by`

The traffic view's caller for each hop normally comes from real trace-context
propagation, with a fallback: ensemble looks at who lists this service in
their own `depends_on`. That fallback has nothing to go on for a service
nothing else needs to *start* (it can come up in any order relative to its
callers) but that other services still call directly at runtime — exactly
`real`, above, if the monolith it replaces predates distributed tracing.
`called_by:` fills the gap explicitly:

```yaml
services:
  monolith:
    called_by: [edge, ops]   # edge and ops call this service directly
```

Each name is a service or gateway (validated the same way `depends_on` is).
A hop attributed this way is flagged **inferred** in the traffic view —
best-effort, never presented as ground truth the way a hop with real trace
context is.

### `kind:` is a badge, not a type

`kind:` (on a service, or a `variants:` entry to override just that one) is
a free-form label the dashboard's Services tab renders as a badge —
`kind: stub`, `kind: mock`, `kind: wip`, whatever's useful to you. Ensemble
never interprets the value; left unset, the badge just reads "service".
Whether something *behaves* as a service, stub, gateway, or database is
decided entirely by which top-level block it's declared under
(`services:`/`stubs:`/`gateways:`/`databases:`) — `kind:` never changes
that, and it exists as a field only on `services:`/`variants:` entries.
`ensemble.yaml` parsing doesn't reject unknown keys, so a stray `kind:` (or
`type:`) under a `stubs:` or `gateways:` entry parses without error and
silently does nothing — there's no field there to receive it.

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

### `on_healthy`: a per-service seed

`health:` proves a process is listening. Some services need one more step
once they are — creating a record only that service can create, provisioning
a queue, priming a cache — before they're actually useful to whatever
depends on them. `on_healthy:` runs once, every time this service's health
gate passes, in the service's own `dir`:

```yaml
services:
  catalog:
    dir: ./services/catalog-svc
    run: ./catalog-svc
    health: /healthz
    on_healthy: >-
      curl -sf -X POST localhost:8081/products
      -H 'content-type: application/json'
      -d '{"name":"seasonal-blend","price_cents":425}'
```

A failing `on_healthy` command fails the service's start the same way a
`build:` failure does — a step that only makes sense once its own
preconditions hold shouldn't leave the service reporting healthy anyway.
It's per-service and fires as soon as *that* service is up; contrast with
`on_ready` (below), which waits for the *whole* stack and runs once per `up`,
not once per service.

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

An `adb`-based `exec:` command (currently just `adb-view`) also accepts
`reverse:`, a list of service/stub/gateway names whose port needs to be
reachable from the device or emulator before the command runs — an Android
deep link that opens straight into calling a local backend needs those
ports `adb reverse`d first, or the app just fails to connect. Each name
resolves through the same port logic a gateway route uses (a service's
proxy/intercept port when retrace is running, its real port otherwise), and
becomes its own `adb reverse tcp:<port> tcp:<port>` step ahead of the
command's own step, `&&`-joined so a failed reverse stops the rest instead
of launching the app against a port that was never actually forwarded:

```yaml
entities:
  widgets:
    base: http://127.0.0.1:4281/widgets
    id: widget_token
    links:
      - label: Open on Android
        kind: exec
        exec: adb-view
        reverse: [gateway, auth]
        template: "myapp://widget/{{widget_token}}"
```

which copies something like:

```
adb reverse tcp:8080 tcp:8080 && adb reverse tcp:9101 tcp:9101 && adb shell am start -a android.intent.action.VIEW -d 'myapp://widget/abc123'
```

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

Sometimes the edge's own path doesn't line up with the backend's — a
client-facing `/v1/orders` the backend actually serves at
`/internal/v1/orders` — or a route needs to carve one specific suffix out of
a broader prefix rather than just stripping it. `regex:` and `rewrite:`
cover both, and are tried after every `prefix:` route, in declaration order,
first match wins — write your own `^`/`$` anchors, since matching isn't
implicitly anchored (e.g. `\.json$` for a suffix match):

```yaml
gateways:
  public:
    port: 9000
    routes:
      - { prefix: /orders, service: order, rewrite: /internal/orders }
      - { regex: '^/v1(/.*checkout)$', rewrite: '/internal/v1$1', service: order }
```

`rewrite:` replaces the matched portion of the path instead of just removing
it. On a `prefix:` route it swaps the matched prefix for `rewrite:`'s value
(the remainder of the path is appended after `/orders/5` → `/internal/orders/5`
above) and is mutually exclusive with `strip_prefix:`. On a `regex:` route
it's a `regexp.ReplaceAllString` template — `$1`, `$2`, … reference the
regex's own capture groups — applied to the whole path, so only the matched
substring changes and the rest is untouched; an empty `rewrite:` on a
`regex:` route leaves the path unmodified, same as no `rewrite:` at all.

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
      allow_headers: ["content-type", "authorization"]
      allow_credentials: false     # a wildcard allow_origins forbids true
      max_age_seconds: 600         # browser caches the preflight result this long
    routes:
      - { prefix: /products, service: catalog }
      - { prefix: /cart,     service: cart,    cors_passthrough: true }
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
ensemble up                     # starts everything, serves the dashboard
ensemble dashboard              # opens it in your browser: topology, live traffic, latency, inspector
ensemble tui                    # or watch it from the terminal instead: services, traffic, latency, profiles
ensemble up --tui                # ...or go straight into the terminal UI once the stack is up
ensemble traffic --follow       # or tail the hops from the terminal, non-interactively
ensemble down
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
id, correlation id, and timings. The same rule applies between services:
an `env:` value that points one service at another's *real* port works
fine — which is what makes the mistake invisible — but those hops bypass
capture entirely. `ensemble up` checks for exactly this and warns when an
`env:` value references another service's real port while that service has
a `proxy:` port, naming both and the port to use instead; the warning also
shows in `ensemble status` (a `warnings` field) and as a badge on the
dashboard's Services tab. It's a warning, never an error — calling a real
port directly is legal, it just isn't observed.

New here? [docs/getting-started.md](docs/getting-started.md) walks a
minimal two-service config through `up`, the dashboard, and a first
recorded flow.

### What the proxy carries — and what it refuses

The proxy speaks HTTP/1.1. Streaming responses (SSE, chunked responses
with no `Content-Length`) flush through write-by-write, so a proxied
event stream is as live as a direct one — and its hop appears in Traffic
marked `streaming` while still open, finalized in place (duration, final
body) when the stream closes. Bodies that aren't valid UTF-8 (images,
PDFs, protobuf) are captured losslessly base64-encoded, so a recorded
binary response replays byte-identical.

What it doesn't carry, it refuses loudly rather than breaking silently:
a WebSocket upgrade or a gRPC request through a proxied port gets an
immediate `501` with a JSON body naming the limitation, and the attempt
is recorded as a hop flagged `unsupported` — a distinct badge in the
traffic view, and a degraded note on any recording session containing
one. Full WebSocket/gRPC/TLS proxying is future work; until then the
first request tells you, instead of a connection that mysteriously hangs.

### Trace and caller headers

Three top-level keys control different parts of how a hop gets attributed —
neighbors, not alternatives, so pick each deliberately:

- **`trace_header:`** (e.g. `x-local-trace-id`) is read as a *fallback trace
  id* whenever a request carries no W3C `traceparent` — for a stack whose
  services don't propagate trace context themselves, so hops still land in
  one trace instead of scattering across synthetic ones. Empty (the
  default) disables the fallback entirely.
- **`source_header:`** (one or more header names, checked in order,
  case-insensitive) lets a caller ensemble doesn't manage — a dev-only
  client, another team's tool — self-declare which service it is, on the
  request itself. Left empty, only the built-in `X-Ensemble-Caller` header is
  checked; set this only if your org already has its own convention (e.g.
  `x-source-client`) to check instead or first.
- **`client_identity_headers:`** (same checked-in-order shape) names the
  header carrying the *client application* that started the request —
  `web`, `ios`, `admin` — shown as a hop's Client in the traffic view.
  Checked on every request regardless of trace context, unlike the two
  above; left empty, `x-source-client` then `x-local-client` are checked. A
  value that fails identifier validation is recorded as `"client"` and never
  stored, so nothing a browser puts in the header reaches disk.

```yaml
trace_header: x-local-trace-id
source_header: [x-source-client]
client_identity_headers: [x-source-client, x-local-client]
```

`source_header` answers "which **service** called this hop" and is a
fallback for missing trace context; `client_identity_headers` answers "which
of our **front-ends** started this" and is always read. A stack commonly
wants one and not the other.

### Injecting latency

```sh
ensemble latency set --target catalog --path / --fixed 400 --enabled
ensemble latency arm-all --enabled=false      # disarm everything at once
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
ensemble latency from-datadog \
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
ensemble latency apply production
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

### `on_ready`: seeding once the whole stack is healthy

Some setup only makes sense once *every* service and database in the stack
is up — data that spans several services, warming a shared cache,
announcing readiness to something outside ensemble entirely. A top-level
`on_ready:` key runs once, after `ensemble up` has confirmed every active
node clean:

```yaml
on_ready:
  seeds: [baseline]                # names from seeds: below, run in declared order
  run: ./tools/announce-ready.sh   # then a plain shell command, in Config.Dir

seeds:
  baseline:
    sql:
      - database: shared-pg
        file: ./seeds/products.sql
      - database: shared-pg
        file: ./seeds/users.sql
        target_db: users            # a second logical database on the same
                                     # postgres instance — target_db overrides
                                     # shared-pg's own default (its POSTGRES_DB)
                                     # for just this one step
```

`seeds:` runs each named seed through the same mechanism `ensemble seed
<name>` uses by hand — every SQL step, then every HTTP step, in the order
they're declared — before `run:` (if set) executes. Both are optional and
may be combined. `on_ready` stops at its first failure, and never runs at
all if any service or database failed to come up — a seed or postinstall
step that assumes the stack it's given shouldn't run against one that
isn't. See `sample/ensemble.yaml`'s own `on_ready:` for a complete, working
example.

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

### Freshness

A stack cloned across several repos drifts silently: someone pushes to the
branch you're tracking, or `main` moves ahead of your feature branch, and the
only way to notice is `cd`-ing into each service and running `git fetch &&
git status` by hand. A top-level `freshness:` key runs that check in the
background instead:

```yaml
freshness:
  default_branch: main   # what "up to date with upstream" means (default main)
  poll_interval_s: 300   # how often to re-fetch (default 300 = 5 minutes)
```

Once every `poll_interval_s`, ensemble runs `git fetch origin` (read-only —
never pull, merge, or rebase) against every service whose `dir:` is its own
git repository, distinct from the repo containing `ensemble.yaml` — a stub
or script living in the config's own repo is always at whatever commit
local-stack is, so it's skipped. Each check reports how many commits the
service is behind its own remote branch and behind `default_branch`,
surfaced as a `freshness` field on `ensemble status`/`ensemble status
--json`, as freshness badges on the dashboard's Services tab (with a "check
freshness" button for an immediate re-check), and via `ensemble freshness`:

```
SERVICE     BRANCH             BEHIND  MAIN BEHIND  CHECKED               ERROR
catalog     feature/discounts  0       12           2026-08-27T10:04:00Z
payments    main               3       0            2026-08-27T10:04:00Z
```

A fetch failure (VPN down, SSH key not forwarded) never reports a false
"up to date" — it degrades to an `error` alongside whatever was last
successfully known, or to "never checked" for a service that has never
succeeded.

### When a service dies

A process that exits after a successful start doesn't linger as a stale
`healthy`: the orchestrator records its exit code (or signal) and time,
and the service moves to `exited` (clean zero) or `crashed` (anything
else) — both distinct from `stopped`, which is reserved for an exit *you*
asked for. `lastErr` carries the tail of the service's log, the change
streams over SSE so the dashboard and TUI update without a refresh, and
`ensemble ready` fails fast on a `crashed` service instead of waiting out
its timeout. Nothing auto-restarts — same philosophy as freshness never
pulling: recovery is a deliberate action (`ensemble restart <service>`),
not something that happens behind your back.

The logs behind that tail are readable directly, not just on disk:
`GET /api/services/{name}/logs?tail=N` returns the last N lines of
`.ensemble/run/<name>.log` (default 200) and
`GET /api/services/{name}/logs/stream` follows it over SSE — build output,
`on_healthy` hooks, and the process's own stdout/stderr, since they all
land in the same file. The dashboard's Services tab has a log pane per
service (tail + follow), and in the TUI `l` opens a log tail on the
selected service.

### CLI

```
ensemble up [-c ensemble.yaml] [--profile p1,p2] [--api 127.0.0.1:4700] [--tui]
ensemble dashboard [--no-open]
ensemble tui
ensemble status | freshness | ready [--timeout DURATION] | down | seed <name>
ensemble restart <service>
ensemble variant <service> <name>
ensemble profiles
ensemble latency list | set | reset | arm-all | from-datadog | apply <profile>
ensemble traffic [--since N] [--errors-only] [--follow] [--session ID] [--export har]
ensemble trace <traceId> [--export har|curl|raw]
```

`ensemble ready` blocks until the stack's readiness checks resolve (or
`--timeout` elapses), exiting 0/1 — the deterministic gate for CI:
`ensemble up && ensemble ready && pnpm test:e2e`.

Traffic isn't limited to what's still in memory: the live view is backed
by an in-memory ring, but every hop is also persisted to
`.ensemble/hops.jsonl`, and `GET /api/traffic/history` reads it back —
newest-first, paginated (`before=<seq>` + `limit`), with the same filters
as `GET /api/traffic` — so yesterday's traffic is a query away, and the
dashboard's Traffic view pages into it with "load earlier". A whole
session exports as one HAR (`GET /api/sessions/{id}/export?format=har`,
or `ensemble traffic --session <id> --export har`), covering every hop
that carried the session's id across ring and history — the multi-trace
counterpart to `ensemble trace <traceId> --export har`.

Every command takes `--json`, and every one is a thin client over the REST API,
so anything the CLI does an agent or script can do over HTTP. `ENSEMBLE_API`
sets the default endpoint. The control plane binds loopback only.

### retrace CLI

```
retrace run [--flow NAME | --flows A,B] [--app NAME] [--ensemble URL] [--upstream URL] [--json] [-- <test command>]
retrace diff --flow NAME [--app NAME] [--a SELECTOR] [--b SELECTOR] [--json] [--out DIR] [--no-fail]
retrace replay --ref FLOW [--app NAME] [--listen 127.0.0.1:0] [--json] -- <test command>
retrace revalidate --ref FLOW [--app NAME] --upstream URL [--json]
retrace ref list|accept|reject [--flow NAME] [--app NAME] [--run SELECTOR] [--json]
retrace ref rule --field GLOB --matcher NAME [--method M] [--path GLOB] [--why TEXT] [--json]
retrace rekey --old KEY --new KEY | --init [--json]
retrace serve [--addr 127.0.0.1:4800] [--allow-host HOST] [--open]
retrace export --out DIR [--flow NAME] [--app NAME] [--json]
retrace runs [--app NAME] [--flow NAME] [--state STATE] [--json] [--abandoned-after DUR]
retrace check [--url URL] [--app NAME] [--flow NAME] [--json] [--abandoned-after DUR]
retrace sync --from github --repo ORG/REPO [--repos A,B] [--workflow NAME] [--since 7d] [--dry-run] [--json]
```

`run` records a flow (or, with neither `--flow` nor `--flows`, every flow in
`retrace.yaml`) against a live stack; `diff` compares two runs pixel/wire/hop
and exits 0/1/2/3 (no diff / differences to review / hard gate failed / could
not evaluate — see `retrace --help` for the full exit-code table); `replay`
serves a blessed recording as strict mocks with no stack running at all, for
CI; `ref` promotes or rejects a run as the reference to diff future runs
against. `runs`/`check` are the supervision half — `runs` lists every
recording and whether it's complete, still running, or abandoned (a capture
that died without finalizing); `check` answers "who owns this port" the way
`lsof` can't, and is the gate to run before binding a marker door yourself.
`export` writes a run's HAR/screenshots out for sharing; `rekey` rotates the
key used by `encrypt`-mode redact rules; `sync` pulls run metadata from a
CI provider (currently GitHub Actions) for `runs`/`check` to reason about
recordings captured in CI rather than locally.

Like `ensemble`, every command takes `--json`; `ENSEMBLE_API` is read for the
`--ensemble` default.

**Recordings are secret-safe by default.** Redaction happens at capture,
never post-hoc, and the defaults cover the places credentials actually
travel: sensitive headers, secret-keyed query parameters, and — when a
body parses as JSON — any object key on the same secret list
(`access_token`, `password`, `client_secret`, …), case-insensitive, at any
nesting depth, in both requests and responses. Your own `redact:` rules
layer on top (destroy or `encrypt` per field), and a stack that
legitimately records fixture credentials can opt the body defaults off
with `redact: { body_defaults: off }`. Redacting never breaks replay: a
recorded `[redacted]` value matches any live value (the built-in
`redacted` matcher), asserting "something secret was here" rather than
its content. And because a reference bundle is made to be committed,
`retrace ref accept` scans the staged exchanges for anything that slipped
past — unredacted secret-keyed values, JWT-shaped strings, AWS key ids,
`Bearer` tokens — and refuses the promotion with the field path and the
rule command that fixes it; `--force` overrides and permanently records
`acceptedWithSecrets: true` in the reference manifest.

The full record → accept → commit → diff → re-accept loop across commits
is walked through in
[docs/reference-lifecycle.md](docs/reference-lifecycle.md), and
[docs/retrace-ci-example.yml](docs/retrace-ci-example.yml) is a
copy-in CI workflow covering both recording in CI and replaying the
committed reference as strict mocks.

## Layout

| Path | What |
| --- | --- |
| `core/` | Go shared module: trace model, proxy, stub engine |
| `ensemble/` | the runner: orchestrator, REST/SSE server, inspector, CLI |
| `retrace/` | record/replay/diff: engines, review server, CLI |
| `dashboard/` | React UIs, embedded into the binaries |
| `openspec/` | specs and roadmap |

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).
