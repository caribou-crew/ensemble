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

**Greenfield, in active build. `ensemble` runs; `retrace` is not written yet.**

| Area | State |
| --- | --- |
| trace model + capturing proxy + stub engine (`core/`) | working |
| orchestrator, REST/SSE API, CLI (`ensemble/`) | working |
| dashboard — topology, traffic, latency, inspector, entities | working |
| `retrace` record/replay/diff/review | **not started** — CLI is a stub |
| sample stack, test-runner adapters | **not started** |
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
./ensemble traffic --follow       # or tail the hops from the terminal
./ensemble down
```

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

### CLI

```
ensemble up [-c ensemble.yaml] [--profile p1,p2] [--api 127.0.0.1:4700]
ensemble dashboard [--no-open]
ensemble status | down | seed <name>
ensemble latency list | set | reset | arm-all
ensemble traffic [--since N] [--errors-only] [--follow]
ensemble trace <traceId> [--export har|curl|raw]
```

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
