# Proposal: init-ensemble-retrace

## What

Create the `ensemble` monorepo: a greenfield rebuild of two existing prototypes
(`local-stack` and `flowlens`, surveyed 2026-08-19) into two published products
sharing one core:

- **ensemble** — run an entire backend topology locally (native processes +
  containerized databases), with every service fronted by a lightweight Go
  proxy that captures hop-by-hop telemetry (trace ids, correlation ids,
  timings, headers/bodies), injects artificial latency, and serves stubs for
  dependencies that can't run locally (AWS-only capabilities, crypto,
  third-party/analytics calls). Controlled via CLI, REST API (LLM-first), and
  a web dashboard (topology graph, traffic viewer, latency controls, DB
  inspector, generic entity pages).
- **retrace** — test-automation integration: record a test run's screenshots +
  full network hop chain as a recording, replay blessed recordings as strict
  CI mocks (no stack needed in CI), and diff runs (pixel, wire, hop) against
  references, with a keyboard-driven review queue for accepting, rejecting, or
  rule-ing differences. LLM-drivable via the same REST/JSON surfaces.

Plus a deep polyglot **sample stack** (2 clients, edge, 2 BFFs, 4 services,
Postgres/MySQL/Redis/DynamoDB Local, 3 stubs) that demonstrates the config
contract and dog-foods both products in CI.

## Why

- The existing prototypes prove the ideas but can't ship: `local-stack` is a
  sanitized export missing its entire orchestration/proxy layer (`tools/`
  tree) and is hardwired to one fintech domain; `flowlens` is a week old,
  Node-based (slow/heavy in CI), with a bless/review flow the author finds
  clunky.
- A shared recording/trace schema between the runner and the test tool is the
  core insight (mocks that are *actual dataflow*, not hand-maintained — the
  evolution of the author's earlier `mezzo` project). Building them together
  around one Go core eliminates drift by construction.
- Go static binaries give the memory efficiency, single-file CI install, and
  npm-wrapped distribution the products need.

## Capabilities touched (all ADDED — greenfield)

`core-trace-model`, `ensemble-proxy`, `ensemble-orchestrator`,
`ensemble-api-dashboard`, `retrace-capture-replay`, `retrace-diff-review`,
`adapters`, `sample-stack`, `distribution`.

## Out of scope (explicitly deferred)

- Fuzzy attribution of hops when services drop trace headers (detect + flag
  only; no timing/connection inference).
- Dashboard React plugin tabs loaded from user code (config-declared entity
  pages only).
- gRPC/WebSocket interception (HTTP/1.1 + JSON first; design leaves room).
- `retrace compare --refs A B` orchestrated two-commit checkout/build/run.
- Windows-native process supervision niceties (binaries build for Windows;
  first-class support is macOS/Linux).
