# ensemble

Run your entire backend stack locally — observed. Two products, one core:

- **ensemble** — a topology-agnostic local-stack runner. Point it at your
  services (jars, node, go, containers) with one `ensemble.yaml`; it fronts
  each with a lightweight Go proxy that captures hop-by-hop telemetry (trace
  ids, correlation ids, timings), injects latency on demand, stubs the
  dependencies you can't run locally, and serves a dashboard + REST API + TUI.
- **encore** — record a test run's screenshots and full network hop chain,
  replay blessed recordings as strict mocks in CI (one static binary, no
  stack), and diff runs — pixel, wire, and hop — against references, with a
  PR-style review queue. Play it again.

Successor to [mezzo](https://github.com/caribou-crew/mezzo): mocks that are
actual recorded dataflow instead of hand-maintained fixtures.

## Status

Greenfield, in active design/build. Specs live in [`openspec/`](openspec/) —
start with `openspec/changes/init-ensemble-encore/design.md`.

## Layout

| Path | What |
| --- | --- |
| `core/` | Go shared module: trace model, proxy, stub engine |
| `ensemble/` | the runner: orchestrator, REST/SSE server, inspector, CLI |
| `encore/` | record/replay/diff: engines, review server, CLI |
| `dashboard/` | React UIs, embedded into the binaries |
| `adapters/` | thin npm packages for test runners (Playwright, Maestro, JS) |
| `sample/` | "brew" — deep polyglot demo stack that dog-foods everything |
