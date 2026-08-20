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
  placement: 'native' | 'docker';
  pid?: number;
  proxyPort?: number;
  port?: number;
  startedAt?: string;
  lastErr?: string;
}

export interface TopologyNode {
  name: string;
  category: 'service' | 'database' | 'stub';
  status: string;
  entry?: boolean;
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
