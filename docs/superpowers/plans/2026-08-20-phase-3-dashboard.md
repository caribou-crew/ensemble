# Phase 3: ensemble Dashboard Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use
> superpowers:subagent-driven-development (recommended) or
> superpowers:executing-plans to implement this plan task-by-task. Steps use
> checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship the embedded React dashboard (topology, traffic, latency,
inspector, entity pages) served single-origin from the `ensemble` binary,
consuming the Phase 2 REST/SSE API.

**Architecture:** A pnpm workspace app `dashboard/ensemble-ui` (Vite + React
19 + TS strict) plus a tiny `dashboard/design-system` package (CSS tokens +
primitives). Vite builds straight into `ensemble/server/ui/dist`, which the
server embeds via `go:embed` and serves at `/` with SPA fallback. Pure
layout/timeline algorithms port from `local-stack/web` with their tests
(inventory: `.superpowers/phase-3-porting-inventory.md`). One Go task adds
the inspector + entity-passthrough endpoints the UI needs (API-first).

**Tech Stack:** React 19, Vite 6, TypeScript strict, vitest (+ happy-dom for
the few component tests), plain CSS with custom-property tokens (no
component/router/query libraries — deliberate, carried from the prototype),
@fontsource IBM Plex Sans/Mono. Go: stdlib only.

**Spec:** `openspec/changes/init-ensemble-encore/specs/ensemble-api-dashboard/spec.md`
(+ design.md §4.5, §4.6). Roadmap boxes 3.1–3.4 in
`openspec/changes/init-ensemble-encore/tasks.md`.

## Global Constraints

- API-first parity: every dashboard capability is a REST/SSE JSON call the
  CLI/agents could make identically. UI never talks to services directly —
  entity pages go through the server passthrough.
- Go stdlib-first; no new Go deps. TS deps limited to: react, react-dom,
  vite, @vitejs/plugin-react, typescript, vitest, happy-dom,
  @fontsource/ibm-plex-sans, @fontsource/ibm-plex-mono. Justify anything else.
- `go test -race ./core/... ./ensemble/... ./encore/...` AND `pnpm -r test`
  green at every commit (repo root; bare `./...` does not resolve there).
  gofmt/go vet clean.
- TDD: ported algorithm tests land BEFORE the ported implementation
  compiles against them (port test → RED → port impl → GREEN).
- Redaction: capture-side only. The UI renders `[redacted]` and `$enc:v1:`
  markers with masked styling; the reveal-eyeball ships with task 4.8, NOT
  here — leave the styling hook (`.redacted` class + title tooltip).
- Dark-mode-only, token-driven CSS (port the prototype's `:root` token
  system + colorblind-verified category palette). No 2,800-line global CSS:
  one CSS file per component/view, tokens in design-system.
- URL-as-state deep links (`?view=&trace=&db=&table=&entity=`) are a
  feature: preserve via a tiny `urlState.ts` util (history.replaceState),
  no router library.

## Exact API contract the UI consumes (verified against ensemble/server)

Endpoints (all JSON; errors are `{"error":"..."}` with 4xx/5xx):

```
GET  /api/health                        → {ok, version}
GET  /api/status                        → {services: ServiceState[]}
GET  /api/topology                      → {nodes: TopologyNode[], edges: TopologyEdge[]}
POST /api/services/{name}/restart|flip
POST /api/seed/{name}                   → {ok, results: SeedStepResult[]} (500 keeps partial results)
GET  /api/traffic?since&limit&errorsOnly&session → {hops: Hop[]}
GET  /api/traffic/stream?since=<seq>    → SSE, `event: hop`, data = Hop JSON, `: heartbeat` comments every 15s
GET  /api/traces/{traceId}              → {hops: Hop[], logical: LogicalHop[]}
GET  /api/traces/{traceId}/export?format=har|curl|raw
GET  /api/latency                       → {rules: LatencyRule[]}
PUT  /api/latency  DELETE /api/latency?target&path
POST /api/latency/arm-all {enabled}  POST /api/latency/reset
POST /api/sessions  DELETE /api/sessions/{id}  GET /api/sessions/{id}/hops
GET  /api/openapi.json
```

TS mirrors of the Go JSON (write these EXACTLY in `src/api/types.ts`):

```ts
export interface Timings { start: string; firstByteMs?: number; doneMs?: number }
export interface Payload { headers?: Record<string, string>; body?: string; truncated?: boolean }
export interface Hop {
  schema: string; seq: number; traceId?: string; spanId?: string;
  parentSpanId?: string; correlationId?: string; session?: string;
  from?: string; to: string; method?: string; path?: string; status?: number;
  t: Timings; req?: Payload; resp?: Payload;
  injectedDelayMs?: number; err?: string;
}
export interface ServiceState {
  name: string; status: string; placement: 'native' | 'docker';
  pid?: number; proxyPort?: number; port?: number;
  startedAt?: string; lastErr?: string;
}
export interface TopologyNode { name: string; category: 'service'|'database'|'stub'; status: string; entry?: boolean }
export interface TopologyEdge { from: string; to: string }
export interface Topology { nodes: TopologyNode[]; edges: TopologyEdge[] }
export interface LatencyRule {
  target: string; path: string; fixedMs?: number;
  p50?: number; p95?: number; p99?: number; enabled: boolean;
}
export interface LogicalHop { hop: Hop; origin: Hop | null; via?: string[]; index: number; statusMismatch?: boolean }
// Task 3.4 additions:
export interface Column { name: string; type: string }
export interface Table { name: string; columns: Column[] }
export interface ChangeEvent { db: string; table: string; at: string }
```

## File Structure

```
dashboard/design-system/         package @ensemble/design-system (private)
  package.json  tokens.css  primitives.tsx (Badge, Tabs, Kbd, Spinner)
dashboard/ensemble-ui/           package ensemble-ui (private)
  package.json  vite.config.ts  tsconfig.json  index.html
  src/main.tsx  src/App.tsx  src/App.css
  src/api/types.ts  src/api/client.ts  src/api/sse.ts
  src/urlState.ts (+ urlState.test.ts)
  src/topology/{categories,layout,hopTimeline,traceLayout}.ts (+ .test.ts each, ported)
  src/topology/fixtures.ts
  src/views/{TopologyView,TrafficView,LatencyView,InspectorView,EntityView}.tsx (+ .css each)
  src/components/{TopologyGraph.tsx,HopDetail.tsx,HopTable.tsx,JsonView.tsx}
ensemble/server/ui/ui.go         go:embed + SPA fallback handler
ensemble/server/ui/dist/index.html   committed placeholder ("UI not built")
```

Vite `build.outDir: '../../ensemble/server/ui/dist'` with `emptyOutDir:
true`; `.gitignore` gets `ensemble/server/ui/dist/*` with
`!ensemble/server/ui/dist/index.html` NOT excluded — instead the placeholder
is overwritten by real builds and `git checkout -- ensemble/server/ui/dist`
restores it; simplest rule: ignore `ensemble/server/ui/dist/assets/` and
track only `index.html` (real builds rewrite it locally; do not commit the
built version — verify with `git diff --stat` before commit).

---

### Task 3.1: Workspace scaffold, design tokens, embed + single-origin serve

**Files:**
- Create: `dashboard/design-system/{package.json,tokens.css,primitives.tsx}`
- Create: `dashboard/ensemble-ui/{package.json,vite.config.ts,tsconfig.json,index.html,src/main.tsx,src/App.tsx,src/App.css,src/api/types.ts,src/api/client.ts,src/urlState.ts,src/urlState.test.ts}`
- Create: `ensemble/server/ui/ui.go`, `ensemble/server/ui/dist/index.html` (placeholder), `ensemble/server/ui/ui_test.go`
- Modify: `ensemble/server/server.go` (mount UI handler for non-/api paths), root `.gitignore`

**Interfaces:**
- Produces: `ui.Handler() http.Handler` — serves embedded dist with SPA
  fallback (any non-file path → index.html; `/api/*` never reaches it).
- Produces: `api/client.ts` — `api.status()`, `api.topology()`,
  `api.traffic(params)`, `api.trace(id)`, `api.latencyList/Upsert/Delete/ArmAll/Reset()`,
  `api.restart(name)`, `api.flip(name)`, `api.seed(name)` — thin typed fetch
  wrappers, `throw new ApiError(status, body.error)` on non-2xx.
- Produces: `urlState.ts` — `readParam(key): string|null`,
  `writeParams(patch: Record<string,string|null>)` (history.replaceState),
  `useUrlParam(key): [string|null, (v:string|null)=>void]`.

- [ ] **Step 1: design-system package.** `tokens.css` ports the prototype's
  `:root` custom properties (surfaces, text scale, violet accent, status
  colors, the colorblind-verified category palette — copy values from
  `/Users/steven/dev/oss/local-stack/web/src/index.css`). `primitives.tsx`
  exports `Badge({tone,children})`, `Tabs({items,active,onSelect})`,
  `Spinner()`. package.json: name `@ensemble/design-system`, private,
  exports `./tokens.css` and `.` (primitives).
- [ ] **Step 2: ensemble-ui scaffold.** Vite React-TS app; vite.config sets
  `build.outDir: '../../ensemble/server/ui/dist'`, `emptyOutDir: true`, and
  dev `server.proxy: {'/api': 'http://127.0.0.1:4700'}`. App.tsx renders a
  Tabs shell with placeholder views + health poll (`api.status()` every 5s)
  in a header strip. Write `src/api/types.ts` EXACTLY as the contract block
  above.
- [ ] **Step 3: urlState test first.** `urlState.test.ts` (vitest,
  happy-dom): writeParams patches querystring without pushing history;
  readParam round-trips; null deletes the key. RED → implement → GREEN.
- [ ] **Step 4: Go embed, test first.** `ui_test.go`:
  `TestUIServesIndexAndSPAFallback` — httptest server with `ui.Handler()`;
  GET `/` and GET `/some/deep/link` both return 200 with the index
  content; GET `/assets/missing.js` returns 404 (real asset paths must not
  fall back). RED → implement `//go:embed all:dist` + handler → GREEN.
- [ ] **Step 5: mount in server.** In `server.New`, mount `ui.Handler()` at
  `/` (mux pattern `GET /`) so `/api/*` routes keep precedence. Extend one
  existing server test to assert `/` returns 200 HTML.
- [ ] **Step 6: build round-trip.** `pnpm install && pnpm -r build`; verify
  `go build ./ensemble/...` embeds the built dist; run the binary, confirm
  the dashboard shell loads at `http://127.0.0.1:4700/`. Do NOT commit the
  built assets (only the placeholder index.html).
- [ ] **Step 7: full suites + commit** `feat(dashboard): workspace scaffold,
  design tokens, embedded single-origin serve`.

### Task 3.2: Topology view — ported layout algorithms + graph + trace-scoped causal layout

**Files:**
- Create: `src/topology/{categories,layout,hopTimeline,traceLayout,fixtures}.ts` + a `.test.ts` per algorithm (ported from `/Users/steven/dev/oss/local-stack/web/src/topology/`)
- Create: `src/components/TopologyGraph.tsx` (+ paintOrder test), `src/views/TopologyView.tsx` + `.css`
- Modify: `src/App.tsx` (wire view)

**Interfaces:**
- Consumes: `Topology`, `ServiceState`, `Hop` from api/types; old-code
  inventory in `.superpowers/phase-3-porting-inventory.md`.
- Produces: `layoutClustered(topology: Topology, statuses: Map<string, ServiceState>, expanded: Set<string>): GraphLayout`;
  `hopTimeline(hops: Hop[]): HopTiming[]`; `heatTier(n: number): 'normal'|'warm'|'hot'`;
  `layoutTrace(hops: Hop[]): GraphLayout`; `causalHopOrder(hops: Hop[]): Hop[]`.

- [ ] **Step 1: port tests first, adapted to new types.** For each of
  categories/layout/hopTimeline/traceLayout: copy the old `.test.ts`,
  mechanically adapt fixtures from old `StackTopology`/`TraceHop` shapes to
  new `Topology`/`Hop` (old `service`→`to`, old `caller`→`from`, timings
  `startPct/widthPct` math unchanged). Adaptation notes: server now supplies
  `category` per node, so `categories.ts` shrinks to
  `categoryOf(node: TopologyNode): CategoryId` + palette mapping + the
  normalize step; keep its test cases that still apply, drop fintech
  id-inference cases. Run: RED (modules missing).
- [ ] **Step 2: port implementations** file by file until each suite is
  GREEN. Preserve the old files' explanatory comments (they document the
  structural-vs-heuristic choices).
- [ ] **Step 3: TopologyGraph component.** SVG renderer over `GraphLayout`
  (nodes = rounded rects w/ status dot + category color, bundled edges,
  cluster hulls). Port `TopologyGraph.paintOrder.test.ts` (edges under
  nodes under labels). Heat: compute per-service hop counts over the last
  60s of traffic snapshot → `heatTier` → node glow class.
- [ ] **Step 4: TopologyView.** Fetch topology+status (5s poll), render
  graph; clicking a service shows a side panel (state, pid/ports, lastErr,
  restart + flip buttons via `api.restart/flip`). When URL has `?trace=`,
  switch to `layoutTrace(hops)` of that trace (fetch `api.trace(id)`) and
  render the hop timing panel (`hopTimeline` waterfall with
  `injectedDelayMs` shown as a hatched bar segment).
- [ ] **Step 5: suites + commit** `feat(dashboard): topology view with
  ported clustered/causal layouts`.

### Task 3.3: Traffic view — live SSE tail, filters, hop detail, export

**Files:**
- Create: `src/api/sse.ts` (+ sse.test.ts), `src/components/{HopTable,HopDetail,JsonView}.tsx`, `src/views/TrafficView.tsx` + `.css`
- Modify: `src/App.tsx`

**Interfaces:**
- Consumes: `GET /api/traffic`, SSE `/api/traffic/stream?since=` (`event:
  hop`), `GET /api/traces/{id}`, export endpoint.
- Produces: `subscribeHops(since: number, onHop: (h: Hop) => void): () => void`
  — EventSource wrapper that reconnects with the last-seen `seq` as `since`
  after `error` events (1s backoff), returns an unsubscribe closure.

- [ ] **Step 1: sse.ts test first** (vitest with a stubbed global
  EventSource): delivers parsed hops in order; on simulated `error` +
  reconnect, the new EventSource URL carries `since=<last seq>`;
  unsubscribe closes. RED → implement → GREEN.
- [ ] **Step 2: HopTable.** Virtualization-free table (ring is bounded);
  columns: seq, session badge (session id prefix or "ambient"), from→to,
  method+path, status (error styling ≥400 or `err`), doneMs,
  injectedDelayMs badge. Row groups: consecutive hops sharing traceId
  render as one chain group with indent by parentSpanId depth (reuse
  `causalHopOrder` from 3.2).
- [ ] **Step 3: TrafficView.** Follow-mode toggle (SSE via subscribeHops,
  auto-scroll pinned to bottom, pause on user scroll-up), filters: text
  (matches to/path), errors-only, session-only/ambient-only; selecting a
  row opens HopDetail; `?trace=` deep link jumps to topology causal view.
- [ ] **Step 4: HopDetail.** Headers table + body (JsonView pretty-print
  when parseable, raw otherwise, `truncated` banner). Values equal to
  `[redacted]` or starting `$enc:v1:` render with `.redacted` masked style
  (title: "revealed in task 4.8"). Timing strip: start→firstByte→done +
  injected delay. Export buttons: har/curl/raw → open
  `/api/traces/{traceId}/export?format=` in new tab; curl also
  copy-to-clipboard.
- [ ] **Step 5: component test** for HopTable grouping (chain indent order
  from fixture hops — reuse a traceLayout fixture).
- [ ] **Step 6: suites + commit** `feat(dashboard): live traffic view with
  SSE tail, hop detail, export`.

### Task 3.4: Server — inspector endpoints + entity passthrough (Go)

**Files:**
- Create: `ensemble/server/inspect.go`, `ensemble/server/inspect_test.go`, `ensemble/server/entities.go`, `ensemble/server/entities_test.go`
- Modify: `ensemble/server/server.go` (Deps), `ensemble/server/routes.go` (registrations), `ensemble/server/openapi.go` (doc entries), `ensemble/cmd/ensemble/cmd_up.go` (wire Inspector from config.Databases)

**Interfaces:**
- Consumes: `inspector.Inspector` (Register/Schema/Rows/Watch),
  `inspector.NewPostgresDriver(dsn)`, `NewMySQLDriver(dsn)`,
  `NewDynamoDriver(endpoint)`; DSN construction conventions from
  `inspector.NewSQLRunner` (reuse, do not duplicate — export a helper if
  needed).
- Produces (REST):
  - `GET /api/databases` → `{databases: [{name, type}]}` (from cfg.Databases ∩ registered drivers)
  - `GET /api/databases/{name}/schema` → `{tables: Table[]}`
  - `GET /api/databases/{name}/rows?table=&limit=&offset=` → `{rows: []map[string]any}` (limit default 50, cap 500)
  - `GET /api/inspector/stream` → SSE `event: change`, data = `{"db","table","at"}` (same framing/heartbeat as traffic stream)
  - `GET /api/entities` → `{entities: [{name, id}]}` from cfg.Entities (the
    UI's entity-page discovery list).
  - `ANY /api/entities/{name}/{path...}` → reverse-proxies to
    `cfg.Entities[name].Base` (joins path, forwards query/body/method,
    strips hop-by-hop headers). 404 for unknown entity. This is what keeps
    entity traffic on the single origin AND recorded when Base points at an
    intercept port. Note: the discovery route and the passthrough share the
    `/api/entities` prefix — register `GET /api/entities` (exact) and
    `/api/entities/{name}/{path...}` separately; ServeMux prefers the more
    specific pattern.
- `server.Deps` gains `Insp *inspector.Inspector` (nil ⇒ inspector
  endpoints return 501, entity passthrough unaffected).

- [ ] **Step 1: tests first** (`inspect_test.go`): fake Driver (in-memory
  maps) registered on a real `inspector.New()`; assert schema/rows JSON
  shapes, unknown db → 404, nil Insp → 501, rows limit cap. SSE test:
  registered fake driver whose Fingerprint flips after a trigger → one
  `event: change` arrives (pattern-match the existing traffic SSE test).
  `entities_test.go`: httptest upstream echoing method/path/body; assert
  GET and POST pass through with query + body intact, unknown entity 404.
  RED.
- [ ] **Step 2: implement** `inspect.go` + `entities.go`; register routes;
  OpenAPI entries. Watch lifecycle: server owns one `Watch(ctx, 2*time.Second)`
  started lazily on first stream subscriber, fanned out to all SSE clients;
  ctx from server lifetime. GREEN.
- [ ] **Step 3: wire in cmd_up.** Build `inspector.New()`; for each
  cfg.Databases entry by type: postgres/mysql via DSN (same env/port rules
  SQLRunner uses), dynamodb via endpoint. Pass through `server.Deps.Insp`.
  Extend the existing runUp test config with a dynamo-typed database whose
  endpoint is an httptest fake returning empty ListTables, asserting
  `/api/databases` lists it.
- [ ] **Step 4: suites + commit** `feat(ensemble/server): inspector REST/SSE
  + entity passthrough endpoints`.

### Task 3.5: Latency, Inspector, and Entity views (UI)

**Files:**
- Create: `src/views/LatencyView.tsx` + `.css`, `src/views/InspectorView.tsx` + `.css`, `src/views/EntityView.tsx` + `.css`
- Modify: `src/api/client.ts` (+databases/schema/rows/entities calls, inspector SSE), `src/App.tsx`

**Interfaces:**
- Consumes: Task 3.4 endpoints (`/api/databases*`, `/api/inspector/stream`,
  `GET /api/entities` discovery list, entity passthrough) and the
  `LatencyRule` CRUD endpoints.

- [ ] **Step 1: LatencyView.** Rules table (target, path, fixed/p50/p95/p99,
  enabled toggle) with inline add/edit/delete → PUT/DELETE; arm-all and
  reset buttons with confirm; annotation feedback via traffic (mutations
  already emit annotation hops). "APM import" is a disabled placeholder
  button with tooltip "Datadog import — stretch S.1"; DO NOT implement.
- [ ] **Step 2: InspectorView.** DB selector (from `/api/databases`) →
  schema sidebar (tables + columns) → rows table with limit/offset paging;
  subscribe to `/api/inspector/stream`; a change event for the viewed table
  refetches rows and flashes the table name in the sidebar; deep-link
  `?db=&table=`.
- [ ] **Step 3: EntityView.** For each entity from `/api/entities`: list
  page (GET `/api/entities/{name}`, render array of objects as table using
  the `id` field as row key + detail link), detail page (GET
  `.../{id}`, JsonView + delete button, edit as raw-JSON textarea → PUT),
  create (raw-JSON textarea → POST). Generic by design — no per-entity
  code.
- [ ] **Step 4: component test** for LatencyView rule-edit round-trip
  against a stubbed fetch.
- [ ] **Step 5: full suites, `pnpm -r build`, manual smoke** (binary +
  sample config: all five views load, no console errors), **commit**
  `feat(dashboard): latency, inspector, and entity views`.

---

## Execution notes

- Task order is strict: 3.1 → 3.2 → 3.3 → 3.4 → 3.5 (3.4 is Go-only and
  could overlap 3.2/3.3 in principle, but dispatches stay sequential —
  shared tree).
- Roadmap mapping: plan tasks 3.1→box 3.1, 3.2→3.2, 3.3→3.3, 3.4+3.5→box
  3.4. Tick boxes at phase end.
- Rulings inherited from Phase 2 SDD ledger apply. New rulings for this
  plan: dependency-light UI (no router/query/component libs) — ruled, per
  prototype's proven pattern + inventory recommendation; APM import UI
  deferred to stretch S.1; reveal-eyeball deferred to 4.8; snapshot-diff
  change-stream tier stays deferred (poller only, per Phase 2 ruling —
  spec's "two-tier" requirement completes when GUI mutations exist to
  snapshot around, revisit at 4.x/entity pages maturity).
- The prototype's fintech tabs, retro Easter egg, and 2,821-line App.css
  are explicitly NOT ported.
