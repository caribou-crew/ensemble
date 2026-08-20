# Tasks: init-ensemble-encore

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development
> (or superpowers:executing-plans inline). Work phase by phase; before starting a
> phase, generate its detailed step plan (superpowers:writing-plans) from this
> roadmap + `design.md` + the capability specs, save it to
> `docs/superpowers/plans/`, and execute that. TDD throughout. Each numbered task
> below ends in an independently testable, committed deliverable.

**Goal:** Ship the ensemble + encore monorepo per `design.md`.

**Architecture:** Go workspace (`core/`, `ensemble/`, `encore/`) + pnpm workspace
(`dashboard/`, `adapters/`) + `sample/`. One trace schema in `core/trace`
consumed by everything.

**Tech stack:** Go ≥1.23 (stdlib-first: net/http, httputil, image/png, embed),
React 19 + Vite + TS for dashboard, node:test→vitest for TS, GoReleaser.

**Spec:** `openspec/changes/init-ensemble-encore/{proposal,design}.md` + `specs/`

## Global constraints

- Hot paths Go, stdlib-first; justify every third-party Go dep in the PR body.
- One hop schema (`core/trace`), versioned `ensemble/1`; no product-local copies.
- Every dashboard/TUI capability reachable via REST/SSE JSON first (API-first parity).
- Redaction at capture, never post-hoc.
- All CLIs emit `--json` for every read command; exit codes gate CI.
- Golden-test fixtures ported from `/Users/steven/dev/oss/flowlens/test/` and
  `local-stack/web/src/**/*.test.*` wherever a behavior is retained.
- `go test -race ./...` and `pnpm test` green at every commit.

## Phase 0 — Repo scaffolding

- [ ] 0.1 Go workspace (`go.work`, three modules), pnpm workspace, root
      `package.json` scripts, CI (GitHub Actions: go test -race, vitest, lint),
      LICENSE (MIT), CONTRIBUTING stub. Verify: CI green on empty tests.

## Phase 1 — core: trace model + proxy (the load-bearing phase)

- [ ] 1.1 `core/trace`: Hop struct + NDJSON codec + schema version; W3C
      traceparent/baggage parse+stamp helpers (`correlationId`, `encore-run`
      keys). Golden tests from flowlens `hops` fixtures.
      Produces: `trace.Hop`, `trace.ParseCtx(header) Ctx`, `trace.Ctx.Child()`.
- [ ] 1.2 `core/trace`: redaction (default header set + user list, applied at
      construction); body size-capping with truncation markers.
- [ ] 1.3 `core/trace`: relay-collapse (port logic + tests from
      `local-stack/web/src/trace/collapse.ts`).
- [ ] 1.4 `core/trace/export`: HAR 1.2, curl, raw. Golden tests from
      `local-stack/web/src/trace/export.ts` fixtures.
- [ ] 1.5 `core/proxy`: single-process multi-listener reverse proxy; per-hop
      capture (timings: in/first-byte/done) → ring buffer + NDJSON appender +
      SSE broadcaster with cursor replay. Integration test: 3 chained listeners
      → one trace, joined ids.
- [ ] 1.6 `core/proxy`: latency rules (longest-prefix per target+path; fixed +
      p50/p95/p99 sampled distribution), live rule store, injected-delay
      recorded distinctly on the hop.
- [ ] 1.7 `core/stub`: config-defined match→respond engine (method/path,
      status/headers/body_file, Go template option); stub hits emit hops.
- [ ] 1.8 `core/proxy`: sessions — registry, ephemeral client-edge listener
      stamping `encore-run` baggage, hop partitioning (session vs ambient),
      propagation-gap detector → capture-trust verdict. Test: two concurrent
      sessions + ambient traffic, zero cross-contamination.

## Phase 2 — ensemble: orchestrator + API server + CLI

- [ ] 2.1 `ensemble/config`: ensemble.yaml schema + validation + defaults
      (services dir/build/watch/run/port/proxy/env/health/depends_on/docker,
      databases incl. dynamodb + localstack types, stubs, entities, latency,
      seeds, profiles). Table tests over good/bad fixtures.
- [ ] 2.2 `ensemble/orchestrator`: native process supervisor (start in dir,
      env injection, health gate, dependency order, restart, build-if-stale
      via watch globs) + Docker driver (containers for services + databases).
- [ ] 2.3 `ensemble/orchestrator`: live placement flip (run↔docker) keeping
      intercept ports stable; named seeds executor (sql via db drivers, http).
- [ ] 2.4 `ensemble/server`: REST+SSE surface — status/topology/traffic/
      latency/sessions/seed/restart/placement; OpenAPI json served at
      /api/openapi.json. Every mutation also logged as an annotation event.
- [ ] 2.5 `ensemble/inspector`: postgres + mysql drivers (schema, rows,
      change-stream: snapshot-diff + poller); dynamodb driver (tables, scan,
      DynamoDB Streams change-stream) against DynamoDB Local in CI.
- [ ] 2.6 `ensemble/cmd/ensemble`: CLI (`up/down/status/seed/latency/traffic
      --json`) as thin REST client + TUI cockpit (bubbletea: services,
      traffic tail w/ filter + yank-as-curl, seeds).

## Phase 3 — dashboard (ensemble-ui)

- [ ] 3.1 `dashboard/` workspace: Vite + React 19 + shared design-system
      package; go:embed wiring + single-origin serve from ensemble binary.
- [ ] 3.2 Topology view (port layout/categories/heat from
      `local-stack/web/src/topology/*` with their tests) + trace-scoped causal
      layout + hop timing panel.
- [ ] 3.3 Traffic view: live SSE tail, filters, hop detail, chain/flow
      grouping, errors-only, follow, export (HAR/curl/raw via API).
- [ ] 3.4 Latency view (rules CRUD, arm-all/reset, APM import w/ explicit
      apply) + Inspector view (schema/rows/change-stream) + generic entity
      pages from `entities:` config.

## Phase 4 — encore: capture/replay/diff/review

- [ ] 4.1 `encore/run`: `encore run --flow -- <cmd>` — session registration
      w/ ensemble (or standalone capture proxy), env handshake
      (ENCORE_RUN_DIR/ENCORE_PROXY_URL), run-dir writer (manifest, wire.jsonl,
      hops.jsonl, groups.jsonl), capture-trust computation.
- [ ] 4.2 `encore/rules`: wire-rules matchers (uuid/iso8601/http-date/etag/
      integer/semver/custom/ignore/exact). Golden tests from flowlens
      `wire-rules`/`matchers` fixtures.
- [ ] 4.3 `encore/replay`: strict mock server from a reference bundle
      (match via rules; unmatched → fail + miss report); `encore revalidate`
      against a live stack.
- [ ] 4.4 `encore/diff`: pixel (pixelmatch port: thresholds, masks, border
      trim, A/B/overlay/diff PNGs — golden images from flowlens), wire
      (pairing, field-level, LIS reorder), hop (added/removed calls,
      hopRequire), unexpected-status, perf budgets, OpenAPI conformance;
      unified summary + `--json` + exit codes.
- [ ] 4.5 `encore/refs`: compact reference bundles, accept/reject/rule
      mutations, opt-in deviations ledger.
- [ ] 4.6 `encore/serve` + `dashboard/encore-ui`: review queue (worst-first,
      keyboard-driven item screen, three verbs) + identical REST verbs;
      `encore export` static report.
- [ ] 4.7 `adapters/`: encore-js (groups/markers), encore-playwright
      (fixture: checkpoint shots + groups), encore-maestro (HTTP markers);
      strict-mode env handshake failure message.

## Phase 5 — sample stack ("brew")

- [ ] 5.1 Services: catalog-svc (Go/pg), user-svc (Node/pg), order-svc
      (Java/Spring/MySQL, profile `full`), notify-worker (Go/redis),
      storefront-bff + ops-bff (Node, dynamo cart), edge-gw (Go) — real CRUD,
      trace-header forwarding, /healthz each.
- [ ] 5.2 Clients: web-app (React/Vite), rn-app (Expo). Order + browse flows.
- [ ] 5.3 `sample/ensemble.yaml` (the reference config: dbs incl. dynamodb +
      localstack, stubs payment/analytics/kms, entities, profiles) + seeds
      (baseline/empty/bulk/outage).
- [ ] 5.4 Dog-food e2e in CI: encore records a web-app flow against the live
      sample, replays it stackless, diffs — zero unexplained deltas.

## Phase 6 — distribution + docs

- [ ] 6.1 GoReleaser: darwin/linux/windows × arm64/x64, GitHub releases,
      Homebrew tap, curl installer.
- [ ] 6.2 npm wrappers (@ensemble-dev/{ensemble,encore} + platform pkgs,
      bin shims, lockstep versions); adapters depend on encore wrapper.
      Verify in a clean Docker node image with network limited to registry.
- [ ] 6.3 Docs: getting-started (bring-your-own-stack walkthrough), config
      reference generated from schema, encore CI recipe; archive this
      openspec change (`openspec archive init-ensemble-encore`) syncing
      specs/ to openspec/specs/.
