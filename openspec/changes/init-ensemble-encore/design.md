# Design: ensemble + encore

Date: 2026-08-19. Status: approved in brainstorming session; this document is
the written record.

## 1. Products, naming, packaging

Monorepo `ensemble` publishes **two products with a shared core**:

- **ensemble** — the local-stack runner/observer (successor to the
  `local-stack` prototype). CLI: `ensemble`.
- **encore** — record/replay/diff test integration (successor to `the JS prototype`).
  CLI: `encore`. Named for "play it again": capture a performance, replay it
  in CI, compare renditions.

Naming continues the music lineage from the author's earlier `mezzo` project.
"Two products, shared core" was chosen over "one product, two modes" so teams
can adopt encore in CI without ever running ensemble; and over "two independent
apps" because a shared trace/recording schema is the whole point.

## 2. Language decisions

**Go core + TS dashboard + thin TS adapters.**

- All hot paths in Go: proxies, orchestrator, both CLIs, recording/replay,
  diff engines, embedded report/dashboard servers. Rationale: single static
  binaries (~15MB), tiny RSS, ~10ms cold start in CI, `net/http` +
  `httputil.ReverseProxy` + `image/png` + `embed` cover the needs with zero
  deps, trivial cross-compile.
- Rust was considered and rejected: its wins (a few MB less RSS, no GC, faster
  pixel math) are irrelevant for a localhost tool; its costs (async ecosystem
  assembly, slower iteration, worse LLM-codegen reliability for async code)
  are real. Go is the better language for this program.
- encore is a **full Go core**, not a port of the Node prototype. Decided
  because: the prototype is a week old and not battle-tested; its review
  UIs/bless flow are being redesigned anyway; CI speed and zero-dependency
  install are top priorities. The prototype's test *fixtures and scenarios*
  port as golden-test data so their intent survives the rewrite.
- TS only where it must be: the React dashboard (built, then embedded via
  `go:embed`) and in-test-process adapters (Playwright/Maestro/JS API).

## 3. Repo layout

```
ensemble/
├── openspec/                  # specs & change proposals (source of truth)
├── core/                      # Go shared module
│   ├── trace/                 #   trace/hop/recording data model — ONE schema
│   ├── proxy/                 #   capture, latency injection, record, replay
│   └── stub/                  #   generic stub engine
├── ensemble/                  # product 1
│   ├── cmd/ensemble/
│   ├── orchestrator/          #   process+container lifecycle, health, deps
│   ├── server/                #   REST + SSE + serves dashboard
│   └── inspector/             #   DB inspector: postgres, mysql, dynamodb
├── encore/                    # product 2
│   ├── cmd/encore/
│   └── (capture, replay, diff engines, refs, review server)
├── dashboard/                 # React/TS: one design system, two apps
│   ├── ensemble-ui/
│   └── encore-ui/
├── adapters/                  # npm: playwright/, maestro/, js/
├── sample/                    # the demo stack (see §8)
└── docs/
```

Go workspaces (`go.work`) + pnpm workspaces side by side.

## 4. ensemble runtime architecture

### 4.1 Interceptor model

One `ensemble` process opens N proxy listeners — one dedicated port per
configured service (a goroutine + socket each, not N processes). Consumers are
pointed at proxy ports instead of real ports via env injection from the
config. Emulates edge proxies (envoy et al.) with the same mechanism.

### 4.2 Telemetry per hop

The proxy stamps/propagates W3C `traceparent` plus a `correlationId` baggage
entry (join key preserved from the prototype). Per hop it records: timings
(proxy-in, upstream-first-byte, upstream-done), status, headers, bodies
(size-capped; redaction rules applied at capture time). Hops stream over SSE
to dashboard/CLI and append to an on-disk NDJSON ring — the **same format
encore recordings use**. Relay-collapse (folding transparent edge hops) lives
in `core/trace`, ported from the prototype's tested web code.

### 4.3 Config contract: `ensemble.yaml`

The user-supplied topology description. Ensemble stays agnostic to what runs —
jars, node, go, python, containers.

```yaml
services:
  bff:
    dir: ../my-company/bff-service   # working directory (any path)
    build: npm run build             # optional; skipped if fresh per `watch` globs
    run: node dist/main.js           # command executed in dir with injected env
    port: 8003                       # where the real service listens
    proxy: 7003                      # ensemble-assigned intercept port
    env: { DOWNSTREAM_SVC_A: "http://localhost:7004" }  # rewire deps via proxies
    health: /healthz
    depends_on: [svc-a, postgres]
  ledger:
    dir: ~/work/ledger-service
    run: java -jar build/libs/ledger-0.4.2.jar
  payments:
    docker: { image: payments:local, ports: ["8010:8080"] }
    # a service with BOTH run and docker can be flipped between native and
    # container placement live (generalizes the prototype's placement toggle)
databases:
  postgres: { image: postgres:16, port: 5432, seed: ./seeds/init.sql }
  dynamo:   { image: amazon/dynamodb-local, port: 8000, type: dynamodb }
  aws:      { image: localstack/localstack, port: 4566, type: localstack,
              services: [dynamodb, sqs] }   # real emulation vs stub — per-dependency choice
stubs:
  aws-kms:
    port: 7020
    routes:
      - match: { method: POST, path: /encrypt }
        respond: { status: 200, body_file: ./stubs/kms-encrypt.json, template: true }
entities:                            # dashboard plugin slots — generic CRUD pages
  users: { base: "http://localhost:7003/users", id: token }
latency:
  defaults: {}                       # rules also settable live via CLI/REST/dashboard
seeds:
  baseline: { sql: [...], http: [...] }   # named seed targets, generic mechanism
profiles:                            # opt-in service groups (e.g. heavy JDK services)
  full: [ledger]
```

### 4.4 Latency injection

Per-target and per-path rules, longest-prefix match, fixed delay or
p50/p95/p99 distribution; arm-all/reset; settable live from dashboard, CLI,
REST. The prototype's Datadog percentile-pull becomes a pluggable
"latency profile import" (Datadog first) — pull never auto-arms.

Stretch (post-v1, first "plugin" concept): per-service APM mapping in config
(`services.<name>.apm: { provider: datadog, query/url: ... }`) with the API
token supplied via the user's env (never in config or repo). The dashboard's
latency page then offers real p50/p95/p99 from the provider over a chosen
lookback window (e.g. past X days) as one-click rule imports — pulled into
the cache, applied only on explicit user action.

### 4.5 Generic inspector + entity plugin slots

Kept generic (decision: "generic inspector + plugin slots"): schema browse,
table rows, and a two-tier change stream (snapshot diffs around GUI mutations
+ background poller) for **Postgres, MySQL, DynamoDB** (Dynamo change stream
via DynamoDB Streams, supported by DynamoDB Local and LocalStack). The
prototype's fintech CRUD tabs are NOT ported; config-declared `entities:`
render generic list/detail/CRUD pages instead. A full React plugin API is
explicitly deferred.

### 4.6 API-first control plane

Everything the dashboard can do, the REST API exposes; the CLI is a thin
client over it. TUI, dashboard, and agents see identical data (principle
carried over from the prototype's `lcs.mjs --json` design).

## 5. Session isolation (concurrent suites / interactive use)

- Every `encore run` mints a `runId` and registers a session
  (`POST /api/sessions`). Each run gets its own ephemeral client-edge proxy
  port; all traffic entering it is stamped `baggage: encore-run=<runId>`.
- Downstream proxies propagate traceparent+baggage, so hops are partitioned by
  runId into per-run recordings. Parallel suites don't collide; run dirs are
  per-runId so files don't either.
- Interactive traffic carries no session baggage → lands in the "ambient"
  stream (dashboard live traffic), never pollutes recordings.
- **Known limit:** if a service drops trace headers, downstream hops are
  unattributable under parallelism. We detect (hops arriving mid-chain without
  baggage during an active session) and stamp the recording's capture-trust
  verdict `degraded: propagation gap at <service>` — actionable, names the
  service to fix. Timing/connection inference is explicitly deferred.

## 6. encore architecture

### 6.1 Capture

`encore run --flow checkout -- <your test command>` shells out to the user's
existing runner (contract is the directory, not the tool — preserved from the
prototype). With ensemble live: full multi-hop chain recorded. Standalone:
encore's own proxy records the client edge. Same artifact either way:

```
.encore/runs/<app>/<flow>/<runId>/
  manifest.json      # schema version, git sha, device geometry, capture-trust
  shots/<checkpoint>.png
  wire.jsonl         # client-edge requests
  hops.jsonl         # full provider-chain hops (when ensemble was live)
  groups.jsonl       # flow-part markers from adapters
```

References are compact committed bundles under `.encore-ref/`. Redaction
(auth/cookie/set-cookie/dpop + config list) applied at the proxy.

### 6.2 Replay (CI)

`encore replay --ref <flow>` serves the blessed recording as mocks from the
single static binary — no stack in CI. Strict by default: unmatched requests
fail the run (the "client deviated" flag). Wire-rules matchers
(uuid/iso8601/http-date/etag/integer/semver/custom/ignore/exact) decide match
equivalence. `encore revalidate` re-runs old recordings against a live stack
to catch server-side drift.

### 6.3 Diff

- **Pixel**: pixelmatch algorithm ported to Go (stdlib PNG), coarse+fine
  thresholds, masks, uniform-border trim, A/B/overlay/diff panes.
- **Wire**: calls paired on normalized method+path; field- and header-level
  body diff; changed/only-A/only-B/reorder via LIS; wireIgnore + wireRules.
- **Hop**: provider-chain diff — "did this flow grow an extra API call?" —
  the LLM-facing regression signal. hopRequire hard gates.
- All emit human report + `--json`; exit codes gate CI. Also retained from the
  prototype: unexpected ≥400 detection with `expectedStatuses`, OpenAPI
  conformance checking, perf budgets, a11y-tree diff (still flagged as
  device-unverified), capture-trust verdicts banner everywhere.

### 6.4 Review queue (redesign — replaces bless flow + board)

One concept: a PR-style review queue. `encore serve` opens a queue of
flows-with-differences, worst first; passing flows collapsed. Each item is one
keyboard-driven screen (shots A/B/overlay slider, wire+hop diff beneath) with
three verbs:

- **accept** — new recording becomes the reference (compact committed bundle)
- **reject** — it's a bug; copies a repro bundle + failing detail
- **rule** — this field is volatile; appends a wire-rule so it never diffs
  again (the ergonomic win that drains volatile-field noise permanently)

No separate bless mode, no bless tokens. The proposed/approved deviations
ledger becomes opt-in for teams wanting ceremony. The same queue is exposed as
JSON over REST with the same three verbs, so an LLM can walk it. This enables
the target closed loop: LLM makes a change → runs flows → checks pixel/wire/
hop diffs vs reference or earlier commit → accepts or investigates.

## 7. Adapters (npm, in-test-process only)

`@ensemble-dev/encore-playwright` (fixture: checkpoints, groups),
`@ensemble-dev/encore-maestro` (HTTP markers), `@ensemble-dev/encore-js`
(grouping/marker API). Thin: they only mark flow parts and capture
screenshots; all heavy lifting is the binary.

## 8. Sample stack ("brew" — coffee-ordering storefront)

Deep 6+ services (decision: deep over minimal to exercise tracing):

```
clients   rn-app (Expo RN)         web-app (React/Vite)
edge      edge-gw (Go)             ← emulates edge envoy; auth stub
bff       storefront-bff (Node)    ops-bff (Node)
services  catalog-svc (Go, Postgres)
          order-svc (Java/Spring Boot, MySQL)     ← the jar; profile "full"
          user-svc (Node, Postgres)
          notify-worker (Go, Redis queue consumer) ← async leg
storage   postgres ×2 schemas, mysql, redis, DynamoDB Local (bff cart storage)
stubs     payment-gw, analytics, kms (ensemble stub engine)
```

- Money path for demos: rn-app → edge → storefront-bff → order-svc →
  (catalog + user + payment-stub) → redis → notify-worker. Six hops, fan-out,
  async tail.
- Every sample service forwards trace headers (reference implementation of the
  propagation contract, ~5 lines each), has `/healthz`, real CRUD.
- `ensemble seed` named targets: baseline, empty, bulk, outage.
- Java service behind `profiles: [full]` so first run needs no JDK.
- The sample is the e2e bed: encore records/replays/diffs against it in CI —
  the products dog-food each other every commit.

## 9. Testing strategy

- Go table tests for core (trace model, proxy, matchers, diff engines) with
  the JS prototype's fixtures ported as golden data. `go test -race` in CI.
- Vitest for dashboard logic (topology layout, relay collapse — ported from
  the prototype's tested modules).
- Sample-stack e2e loop in CI (record → replay → diff).

## 10. Distribution & release

- **npm-first for node shops**: `npm i -D @ensemble-dev/encore` installs a JS
  wrapper with per-platform `optionalDependencies`
  (`@ensemble-dev/encore-darwin-arm64`, …) each containing the prebuilt
  binary; `bin` shim execs it. No postinstall downloads (locked-registry
  safe). Same pattern as esbuild/turbo/biome. Adapters depend on the wrapper.
  `@ensemble-dev/ensemble` likewise.
- Also GoReleaser → GitHub releases + Homebrew tap + curl script, for
  machine-level installs. darwin/linux/windows, arm64+x64.
- One version number per release across binaries and npm packages.

## 11. Workflow

openspec is the source of truth for specs; future work goes through openspec
change proposals. This change (`init-ensemble-encore`) carries the initial
capability specs; `tasks.md` will sequence implementation:
core schema + proxy → ensemble runner → dashboard → encore → sample stack.
