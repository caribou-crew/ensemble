# Design: ensemble + retrace

Date: 2026-08-19. Status: approved in brainstorming session; this document is
the written record.

## 1. Products, naming, packaging

Monorepo `ensemble` publishes **two products with a shared core**:

- **ensemble** — the local-stack runner/observer (successor to the
  `local-stack` prototype). CLI: `ensemble`.
- **retrace** — record/replay/diff test integration (successor to `flowlens`).
  CLI: `retrace`. Named for what it does to a test run: walk the same path
  again — same hops, same screens — and report where it diverged.

`ensemble` keeps the music lineage of the author's earlier `mezzo` project;
`retrace` is named for the act, not the metaphor, because its job (diff and
replay) is not a musical one. `encore` was the working name and was dropped:
encore.dev ships an active `encore` CLI, so the binary would collide.
"Two products, shared core" was chosen over "one product, two modes" so teams
can adopt retrace in CI without ever running ensemble; and over "two independent
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
- retrace is a **full Go core**, not a port of the Node prototype. Decided
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
├── retrace/                   # product 2
│   ├── cmd/retrace/
│   └── (capture, replay, diff engines, refs, review server)
├── dashboard/                 # React/TS: one design system, two apps
│   ├── ensemble-ui/
│   └── retrace-ui/
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
retrace recordings use**. Relay-collapse (folding transparent edge hops) lives
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

- Every `retrace run` mints a `runId` and registers a session
  (`POST /api/sessions`). Each run gets its own ephemeral client-edge proxy
  port; all traffic entering it is stamped `baggage: retrace-run=<runId>`.
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

## 6. retrace architecture

### 6.1 Capture

`retrace run --flow checkout -- <your test command>` shells out to the user's
existing runner (contract is the directory, not the tool — preserved from the
prototype). With ensemble live: full multi-hop chain recorded. Standalone:
retrace's own proxy records the client edge. Same artifact either way:

```
.retrace/runs/<app>/<flow>/<runId>/
  manifest.json      # schema version, git sha, device geometry, capture-trust
  shots/<checkpoint>.png
  wire.jsonl         # client-edge requests
  hops.jsonl         # full provider-chain hops (when ensemble was live)
  groups.jsonl       # flow-part markers from adapters
```

References are compact committed bundles under `.retrace-ref/`.

### 6.1.1 Redaction modes and recording encryption

Redaction serves two different jobs — protecting eyes (screens, demos) and
protecting the artifact (recordings are committed and shared) — so keys get
per-key modes in a shared `redaction:` config block read by both products.
All modes apply at capture; plaintext never hits disk for encrypt/destroy.

- `display` — stored plaintext, masked in every UI behind a reveal
  (eyeball) click. Screen protection only.
- `encrypt` — field-level AES-256-GCM at capture, stored as
  `$enc:v1:<nonce+ciphertext>`. UIs mask with reveal-on-click (decrypts
  only when the key is present locally). **Replay decrypts at serve time**,
  so mocks keep full fidelity while the committed artifact is safe without
  the key. Default for user-listed body fields.
- `destroy` — irrecoverable, but as a deterministic placeholder
  (`red-<hash8>`, HMAC keyed with a per-recording key that is then
  discarded): the same original value maps to the same placeholder within
  one recording, so value-echo flows still correlate and replay matching
  still pairs. Default for auth-bearing headers
  (authorization/cookie/set-cookie/dpop) — replay never needs them.
- `recordings: encrypt-all` option: encrypt entire wire/hop bodies (opaque
  without retrace's UI/CLI + key). Not the default — field-level keeps
  recordings human-diffable in PRs.

Key model: **team key via env/keyfile** (`RETRACE_RECORDING_KEY` or
gitignored `.retrace/recording.key`), shared through the team's normal
secrets channel; trivial in CI. Envelope encryption underneath — each
recording gets a random data key wrapped by the team key — so `retrace
rekey` rotates cheaply and a public-key recipients model (age/SOPS style)
can be added later without re-recording.

GUI control: config is the source of truth; dashboards add per-field
reveal and an "add redaction rule" action on any hop (edits config, like
the review queue's `rule` verb). Level changes affect future captures
only — nothing retroactively unredacts a destroyed value.

### 6.2 Replay (CI)

`retrace replay --ref <flow>` serves the blessed recording as mocks from the
single static binary — no stack in CI. Strict by default: unmatched requests
fail the run (the "client deviated" flag). Wire-rules matchers
(uuid/iso8601/http-date/etag/integer/semver/custom/ignore/exact) decide match
equivalence. `retrace revalidate` re-runs old recordings against a live stack
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

One concept: a PR-style review queue. `retrace serve` opens a queue of
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

`@caribou-crew/retrace-playwright` (fixture: checkpoints, groups),
`@caribou-crew/retrace-maestro` (HTTP markers), `@caribou-crew/retrace-js`
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
- The sample is the e2e bed: retrace records/replays/diffs against it in CI —
  the products dog-food each other every commit.

## 9. Testing strategy

- Go table tests for core (trace model, proxy, matchers, diff engines) with
  the flowlens prototype's fixtures ported as golden data. `go test -race` in CI.
- Vitest for dashboard logic (topology layout, relay collapse — ported from
  the prototype's tested modules).
- Sample-stack e2e loop in CI (record → replay → diff).

## 10. Distribution & release

- **npm-first for node shops**: `npm i -D @caribou-crew/retrace` installs a JS
  wrapper with per-platform `optionalDependencies`
  (`@caribou-crew/retrace-darwin-arm64`, …) each containing the prebuilt
  binary; `bin` shim execs it. No postinstall downloads (locked-registry
  safe). Same pattern as esbuild/turbo/biome. Adapters depend on the wrapper.
  `@caribou-crew/ensemble` likewise.
- Also GoReleaser → GitHub releases + Homebrew tap + curl script, for
  machine-level installs. darwin/linux/windows, arm64+x64.
- One version number per release across binaries and npm packages.

## 11. Workflow

openspec is the source of truth for specs; future work goes through openspec
change proposals. This change (`init-ensemble-retrace`) carries the initial
capability specs; `tasks.md` will sequence implementation:
core schema + proxy → ensemble runner → dashboard → retrace → sample stack.
