# ensemble

Run your entire backend stack locally — observed. Two products, one core:

- **ensemble** — a topology-agnostic local-stack runner. Point it at your
  services (jars, node, go, containers) with one `ensemble.yaml`; it fronts
  each with a lightweight Go proxy that captures hop-by-hop telemetry (trace
  ids, correlation ids, timings), injects latency on demand, stubs the
  dependencies you can't run locally, and serves a dashboard + REST API.
- **encore** — record a test run's screenshots and full network hop chain,
  replay blessed recordings as strict mocks in CI (one static binary, no
  stack), and diff runs — pixel, wire, and hop — against references, with a
  PR-style review queue. Play it again.

Successor to [mezzo](https://github.com/caribou-crew/mezzo): mocks that are
actual recorded dataflow instead of hand-maintained fixtures.

## Status

**Greenfield, in active build. `ensemble` runs; `encore` is not written yet.**

| Area | State |
| --- | --- |
| trace model + capturing proxy + stub engine (`core/`) | working |
| orchestrator, REST/SSE API, CLI (`ensemble/`) | working |
| dashboard — topology, traffic, latency, inspector, entities | working |
| `encore` record/replay/diff/review | **not started** — CLI is a stub |
| sample stack, test-runner adapters | **not started** |
| published npm / brew packages | not yet released |

Specs live in [`openspec/`](openspec/) — start with
`openspec/changes/init-ensemble-encore/design.md`. The roadmap and its current
state are in `openspec/changes/init-ensemble-encore/tasks.md`.

## Try it

Requires Go 1.25, and Docker only if your config declares databases.

```sh
git clone https://github.com/caribou-crew/ensemble
cd ensemble
go build -o ensemble ./ensemble/cmd/ensemble
```

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

Then:

```sh
./ensemble up                     # starts everything, serves the dashboard
open http://127.0.0.1:4700        # topology, live traffic, latency, inspector
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
| `encore/` | record/replay/diff: engines, review server, CLI (not yet built) |
| `dashboard/` | React UIs, embedded into the binaries |
| `openspec/` | specs and roadmap |

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).
