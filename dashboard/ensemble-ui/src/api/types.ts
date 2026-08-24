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

// One entry in GET /api/entities' discovery list. `id` is the CONFIGURED
// ROW-ID FIELD NAME for this entity (ensemble.yaml's entities.<name>.id,
// e.g. "id" or "uuid") — not any particular row's id value. A row's own
// identifier is read off `row[info.id]` at render time.
export interface EntityInfo {
  name: string;
  id: string;
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
