# Tasks: init-ensemble-retrace

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development
> (or superpowers:executing-plans inline). Work phase by phase; before starting a
> phase, generate its detailed step plan (superpowers:writing-plans) from this
> roadmap + `design.md` + the capability specs, save it to
> `docs/superpowers/plans/`, and execute that. TDD throughout. Each numbered task
> below ends in an independently testable, committed deliverable.

**Goal:** Ship the ensemble + retrace monorepo per `design.md`.

**Architecture:** Go workspace (`core/`, `ensemble/`, `retrace/`) + pnpm workspace
(`dashboard/`, `adapters/`) + `sample/`. One trace schema in `core/trace`
consumed by everything.

**Tech stack:** Go ≥1.23 (stdlib-first: net/http, httputil, image/png, embed),
React 19 + Vite + TS for dashboard, node:test→vitest for TS, GoReleaser.

**Spec:** `openspec/changes/init-ensemble-retrace/{proposal,design}.md` + `specs/`

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

- [x] 0.1 Go workspace (`go.work`, three modules), pnpm workspace, root
      `package.json` scripts, CI (GitHub Actions: go test -race, vitest, lint),
      LICENSE (MIT), CONTRIBUTING stub. Verify: CI green on empty tests.

## Phase 1 — core: trace model + proxy (the load-bearing phase)

- [x] 1.1 `core/trace`: Hop struct + NDJSON codec + schema version; W3C
      traceparent/baggage parse+stamp helpers (`correlationId`, `retrace-run`
      keys). Golden tests from flowlens `hops` fixtures.
      Produces: `trace.Hop`, `trace.ParseCtx(header) Ctx`, `trace.Ctx.Child()`.
- [x] 1.2 `core/trace`: redaction (default header set + user list, applied at
      construction); body size-capping with truncation markers.
- [x] 1.3 `core/trace`: relay-collapse (port logic + tests from
      `local-stack/web/src/trace/collapse.ts`).
- [x] 1.4 `core/trace/export`: HAR 1.2, curl, raw. Golden tests from
      `local-stack/web/src/trace/export.ts` fixtures.
- [x] 1.5 `core/proxy`: single-process multi-listener reverse proxy; per-hop
      capture (timings: in/first-byte/done) → ring buffer + NDJSON appender +
      SSE broadcaster with cursor replay. Integration test: 3 chained listeners
      → one trace, joined ids.
- [x] 1.6 `core/proxy`: latency rules (longest-prefix per target+path; fixed +
      p50/p95/p99 sampled distribution), live rule store, injected-delay
      recorded distinctly on the hop.
- [x] 1.7 `core/stub`: config-defined match→respond engine (method/path,
      status/headers/body_file, Go template option); stub hits emit hops.
- [x] 1.8 `core/proxy`: sessions — registry, ephemeral client-edge listener
      stamping `retrace-run` baggage, hop partitioning (session vs ambient),
      propagation-gap detector → capture-trust verdict. Test: two concurrent
      sessions + ambient traffic, zero cross-contamination.

## Phase 2 — ensemble: orchestrator + API server + CLI

- [x] 2.1 `ensemble/config`: ensemble.yaml schema + validation + defaults
      (services dir/build/watch/run/port/proxy/env/health/depends_on/docker,
      databases incl. dynamodb + localstack types, stubs, entities, latency,
      seeds, profiles). Table tests over good/bad fixtures.
- [x] 2.2 `ensemble/orchestrator`: native process supervisor (start in dir,
      env injection, health gate, dependency order, restart, build-if-stale
      via watch globs) + Docker driver (containers for services + databases).
- [x] 2.3 `ensemble/orchestrator`: live placement flip (run↔docker) keeping
      intercept ports stable; named seeds executor (sql via db drivers, http).
- [x] 2.4 `ensemble/server`: REST+SSE surface — status/topology/traffic/
      latency/sessions/seed/restart/placement; OpenAPI json served at
      /api/openapi.json. Every mutation also logged as an annotation event.
- [x] 2.5 `ensemble/inspector`: postgres + mysql drivers (schema, rows,
      change-stream: snapshot-diff + poller); dynamodb driver (tables, scan,
      DynamoDB Streams change-stream) against DynamoDB Local in CI.
- [x] 2.6 `ensemble/cmd/ensemble`: CLI (`up/down/status/seed/latency/traffic
      --json`) as thin REST client + TUI cockpit (bubbletea: services,
      traffic tail w/ filter + yank-as-curl, seeds).

## Phase 3 — dashboard (ensemble-ui)

- [x] 3.1 `dashboard/` workspace: Vite + React 19 + shared design-system
      package; go:embed wiring + single-origin serve from ensemble binary.
- [x] 3.2 Topology view (port layout/categories/heat from
      `local-stack/web/src/topology/*` with their tests) + trace-scoped causal
      layout + hop timing panel.
- [x] 3.3 Traffic view: live SSE tail, filters, hop detail, chain/flow
      grouping, errors-only, follow, export (HAR/curl/raw via API).
- [x] 3.4 Latency view (rules CRUD, arm-all/reset, APM import w/ explicit
      apply) + Inspector view (schema/rows/change-stream) + generic entity
      pages from `entities:` config.

## Phase 4 — retrace: capture/replay/diff/review

Split into two plans. **Part 1 = boxes 4.1-4.7**, planned in
`docs/superpowers/plans/2026-08-21-phase-4-retrace.md` (18 tasks). **Part 2 =
box 4.8 plus the a11y-tree diff**, deferred to its own plan and enumerated in
part 1's header so it cannot be dropped. 4.8 is split out because it rewrites
`core/trace.Redactor` on ensemble's already-shipped capture path, it is the
only cryptographic surface in the product and deserves one coherent security
review, and it cannot be tested end to end until part 1's replay server and
review UI exist.

- [ ] 4.1 `retrace/run`: `retrace run --flow -- <cmd>` — session registration
      w/ ensemble (or standalone capture proxy), env handshake
      (RETRACE_RUN_DIR/RETRACE_PROXY_URL), run-dir writer (manifest, wire.jsonl,
      hops.jsonl, groups.jsonl), capture-trust computation.
- [ ] 4.2 `retrace/rules`: wire-rules matchers (uuid/iso8601/http-date/etag/
      integer/semver/custom/ignore/exact). Golden tests from flowlens
      `wire-rules`/`matchers` fixtures.
- [ ] 4.3 `retrace/replay`: strict mock server from a reference bundle
      (match via rules; unmatched → fail + miss report); `retrace revalidate`
      against a live stack.
- [ ] 4.4 `retrace/diff`: pixel (pixelmatch port: thresholds, masks, border
      trim, A/B/overlay/diff PNGs — golden images from flowlens), wire
      (pairing, field-level, LIS reorder), hop (added/removed calls,
      hopRequire), unexpected-status, perf budgets, OpenAPI conformance;
      unified summary + `--json` + exit codes.
- [ ] 4.5 `retrace/refs`: compact reference bundles, accept/reject/rule
      mutations, opt-in deviations ledger.
- [ ] 4.6 `retrace/serve` + `dashboard/retrace-ui`: review queue (worst-first,
      keyboard-driven item screen, three verbs) + identical REST verbs;
      `retrace export` static report.
- [ ] 4.7 `adapters/`: retrace-js (groups/markers), retrace-playwright
      (fixture: checkpoint shots + groups), retrace-maestro (HTTP markers);
      strict-mode env handshake failure message.
- [ ] 4.8 Redaction modes + recording encryption: extend `core/trace`
      Redactor with display/encrypt/destroy per-key modes (AES-256-GCM
      `$enc:v1` markers, deterministic `red-<hash>` placeholders); envelope
      key wrapping w/ team key (env/keyfile), `retrace rekey`; replay-time
      decryption; encrypt-all option; reveal-eyeball + add-rule actions in
      traffic and review UIs (ties into 3.3/4.6).
- [ ] 4.9 a11y-tree diff (flagged experimental until device-verified).
      Required by `specs/retrace-diff-review/spec.md` ("SHALL retain: …
      a11y-tree diff") but never tracked as a box until now, and dropped
      from part 1. Belongs to part 2 alongside 4.8.

## Phase 5 — sample stack ("brew")

- [x] 5.1 Services: catalog-svc (Go/pg), user-svc (Node/pg), order-svc
      (Java/Spring/MySQL, profile `full`), notify-worker (Go/redis),
      storefront-bff + ops-bff (Node, dynamo cart), edge-gw (Go) — real CRUD,
      trace-header forwarding, /healthz each.
- [ ] 5.2 Clients: web-app (React/Vite) done — browse/cart/checkout against
      edge-gw, wired into ensemble.yaml, live-tested against the seeded
      stack. rn-app (Expo) not started.
- [x] 5.3 `sample/ensemble.yaml` (the reference config: dbs incl. dynamodb,
      stubs payment/analytics/kms, profiles) + seeds
      (baseline/empty/bulk/outage). Uses DynamoDB Local directly rather than
      localstack, and analytics/kms are unwired decorations (see the stub's
      own comment in ensemble.yaml) — everything on the money path is live
      and integration-tested.
- [ ] 5.4 Dog-food e2e in CI: retrace records a web-app flow against the live
      sample, replays it stackless, diffs — zero unexplained deltas.

## Phase-exit acceptance (user-set, 2026-08-20)

Before this change is called done: web client AND Expo RN client build
cleanly; at least one retrace capture run recorded against the sample
scenario; replay of that recording exercised and green; release automation
exists (see 6.0/6.1).

## Phase 6 — distribution + docs

- [x] 6.0 Release workflow (STARTED EARLY, user request): GoReleaser config
      + `.github/workflows/release.yml` triggered on version tag (`v*`) —
      build both binaries (darwin/linux/windows × arm64/x64), draft GitHub
      release. npm publish steps scaffolded but inert: registry/scope/repo
      target land in a follow-up sync (user syncing on npm details + push
      target). Nothing is pushed or published until that sync.
- [ ] 6.1 GoReleaser: darwin/linux/windows × arm64/x64, GitHub releases,
      Homebrew tap, curl installer.
- [ ] 6.2 npm wrappers (@caribou-crew/{ensemble,retrace} + platform pkgs,
      bin shims, lockstep versions); adapters depend on retrace wrapper.
      Verify in a clean Docker node image with network limited to registry.
- [ ] 6.3 Docs: getting-started (bring-your-own-stack walkthrough), config
      reference generated from schema, retrace CI recipe; archive this
      openspec change (`openspec archive init-ensemble-retrace`) syncing
      specs/ to openspec/specs/.

## Follow-ups (opened during Phase 3, tracked so they are not lost)

- [ ] F.1 Real readiness checks for `redis` and `localstack` databases.
      Task 3.6 replaced the bare TCP dial with a genuine protocol handshake,
      but only for types that have an `inspector.Driver` (postgres, mysql,
      dynamodb). Redis and localstack still gate on a TCP dial, which a
      published docker port answers even when nothing is listening inside the
      container — so "reports healthy while broken" is still live for those
      two. Needs a readiness seam that does not require a full inspector
      driver (redis `PING`, localstack `/_localstack/health`). Landing 3.6
      partially fixed this class of bug; a partial fix to a lying health
      check reads as a complete one, which is why this is tracked here rather
      than in a report.
- [ ] F.2 Validate `databases.<name>.containerPort` bounds in
      `config.Validate` (1-65535). Today an absurd or negative value is
      accepted by config parsing and fails late at `docker run` with a
      docker-level error instead of a config error naming the field.
- [ ] F.3 Count dropped hops in `SessionManager.route`
      (`core/proxy/session.go`) and expose the count so a capture can report
      it. Measured during the Phase 4 Task 5 review: `rep.Hops` is
      snapshotted when `End` deletes the session from the map, and any hop
      routed after that is dropped by `route` with **no counter** — the hop
      vanishes and the run still reports `verdict: "ok"` with an empty
      `trustNotes`. retrace's drain narrows this window but provably cannot
      close it from the outside, so the fix has to live here.
      This is the zero-value trap at system scale: "no hops were lost" and
      "hops were lost and nobody counted them" currently serialize to
      identical bytes. A counter alone is enough to tell them apart — the
      race itself may stay.

## Stretch (post-v1)

- [ ] S.1 APM latency plugin (Datadog first): `services.<name>.apm` config
      mapping, token from user env, dashboard latency page offers provider
      p50/p95/p99 over a lookback window as one-click (explicitly applied)
      rule imports. First exercise of the plugin concept.
