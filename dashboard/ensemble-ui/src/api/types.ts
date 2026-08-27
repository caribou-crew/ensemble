// TS mirrors of ensemble/server's JSON types. Copied verbatim from the
// plan's "Exact API contract the UI consumes" block
// (docs/superpowers/plans/2026-08-20-phase-3-dashboard.md) — do not drift
// from the Go shapes without updating both.

export interface Timings {
  start: string;
  firstByteMs?: number;
  doneMs?: number;
}

export interface Payload {
  headers?: Record<string, string>;
  body?: string;
  truncated?: boolean;
}

export interface Hop {
  schema: string;
  seq: number;
  traceId?: string;
  spanId?: string;
  parentSpanId?: string;
  correlationId?: string;
  session?: string;
  from?: string;
  /** How `from` was resolved when it isn't a real trace-context fact (core/proxy's SpanOwner
   * had nothing to look up): "declared" means the caller self-asserted its name via the
   * X-Ensemble-Caller header (a caller ensemble doesn't manage); "inferred" means it came
   * from a config-declared called_by hint instead, a static guess rather than a live
   * assertion. Absent means `from`, if set, is a trace-derived fact. */
  attribution?: 'inferred' | 'declared';
  to: string;
  method?: string;
  path?: string;
  status?: number;
  t: Timings;
  req?: Payload;
  resp?: Payload;
  injectedDelayMs?: number;
  err?: string;
  /** True when ensemble answered a CORS preflight itself rather than
   * forwarding it to an upstream — never set on real proxied traffic. */
  preflight?: boolean;
  /** The client APPLICATION that sent this request — "web", "ios", "admin" —
   * read from the first configured client-identity header present
   * (`client_identity_headers:`, default x-source-client then x-local-client).
   * Populated wherever that header arrives, which in practice is the entry
   * hop: an internal call carries it only if that service forwards it.
   *
   * Not the same thing as `from`, which is a position in the service graph
   * and a fallback for missing trace context. `client` is validated to
   * `^[a-z0-9][a-z0-9:-]{0,31}$` at capture, so unlike `from` it is safe to
   * group and filter on; a value that failed validation arrives as the
   * literal "client" and the original is never stored. */
  client?: string;
}

export interface ServiceState {
  name: string;
  status: string;
  placement: "native" | "docker";
  pid?: number;
  proxyPort?: number;
  port?: number;
  startedAt?: string;
  lastErr?: string;
  /** Current `variants:` choice, for a service that declares any. */
  variant?: string;
  /** Sampled RSS in KB. Only populated when status was fetched with `?mem=1`. */
  rssKB?: number;
  /** Free-form label from config.Service/Variant `kind:`, e.g. "stub", "mock". Empty means
   * "service" (a real, unlabeled backing). Named kind, not type, to avoid colliding with
   * config.Database's `type:` (a validated engine enum — postgres, redis, etc). */
  kind?: string;
  /** How far this service's checkout is behind its own remote branch and behind the
   * configured default branch — absent unless `freshness:` is configured AND the service is
   * eligible (its own git repo, distinct from the one containing ensemble.yaml). Populated by
   * a background poll, not computed at read time — see FreshnessState. */
  freshness?: FreshnessState;
}

/** One service's git-freshness snapshot — mirrors
 * ensemble/orchestrator.FreshnessState's JSON shape exactly. */
export interface FreshnessState {
  branch: string;
  behindBranch: number;
  behindDefault: number;
  defaultBranch: string;
  /** RFC3339 timestamp of the last SUCCESSFUL check. Empty means this service has never been
   * successfully checked — render as "unknown", never as "up to date". */
  checkedAt?: string;
  /** Set when the most recent check attempt failed (fetch failure, or the branch/rev-list
   * comparison couldn't be resolved). May appear alongside a populated branch/behind* from an
   * earlier successful check — that's "this is what we last knew, and the most recent
   * recheck failed", not a fresh empty answer. */
  error?: string;
}

export interface TopologyNode {
  name: string;
  category: "service" | "database" | "stub" | "gateway";
  status: string;
  entry?: boolean;
  /** Set only for a service declaring `variants:` — the current choice and every option. */
  variant?: string;
  variants?: string[];
  /** "gateway" nodes only — mirrors config.Gateway.ExposeInTraffic. When false (the default),
   * the Traffic tab collapses this gateway's own hop into its target's. */
  exposeInTraffic?: boolean;
}

export interface TopologyEdge {
  from: string;
  to: string;
}

export interface Topology {
  nodes: TopologyNode[];
  edges: TopologyEdge[];
}

export interface LatencyRule {
  target: string;
  path: string;
  fixedMs?: number;
  p50?: number;
  p95?: number;
  p99?: number;
  enabled: boolean;
}

export interface LogicalHop {
  hop: Hop;
  origin: Hop | null;
  via?: string[];
  index: number;
  statusMismatch?: boolean;
}

// Task 3.4 additions:
export interface Column {
  name: string;
  type: string;
  nullable: boolean;
}

export interface Table {
  name: string;
  columns: Column[];
}

export interface ChangeEvent {
  db: string;
  table: string;
  at: string;
}

// Task 3.5 additions:

export interface DatabaseInfo {
  name: string;
  type: string;
}

// One configured "open in host app" button for an entity's rows — mirrors
// ensemble/server's entityLink. Template is a plain string with
// {{column}} placeholders resolved client-side against each row's own
// fields — see format.ts's resolveLinkTemplate.
//
// `kind` is absent (or "url") for the original behavior: resolve and
// navigate/open directly. `kind: "exec"` instead means `steps` is the
// target command's steps, each an argv template (exactly one element
// across all of them is the literal sentinel "{{url}}") — the button
// builds and copies a local CLI command to the clipboard, one step per
// `&&`-joined shell command, rather than navigating. Any `adb reverse`
// steps from the config's `reverse:` list are already resolved into
// `steps` by the server — the client never sees `reverse:` or a port
// number to resolve itself. See format.ts's buildExecCommand.
export interface EntityLink {
  label: string;
  template: string;
  kind?: 'exec';
  steps?: string[][];
}

// One entry in GET /api/entities' discovery list. `id` is the CONFIGURED
// ROW-ID FIELD NAME for this entity (ensemble.yaml's entities.<name>.id,
// e.g. "id" or "uuid") — not any particular row's id value. A row's own
// identifier is read off `row[info.id]` at render time.
export interface EntityInfo {
  name: string;
  id: string;
  links?: EntityLink[];
}

// Not in the plan's contract block, but required to type POST
// /api/seed/{name}'s response — mirrors
// ensemble/orchestrator.SeedStepResult's JSON tags exactly
// (ensemble/orchestrator/seed.go).
export interface SeedStepResult {
  kind: string;
  ref: string;
  ok: boolean;
  err?: string;
  durationMs: number;
}

/** GET /api/profiles — every configured profile with its members and live state. */
export interface ProfileInfo {
  name: string;
  services: string[];
  active: boolean;
}

export interface ProfilesState {
  active: string[];
  profiles: ProfileInfo[];
}
