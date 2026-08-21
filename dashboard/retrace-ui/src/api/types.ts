// TS mirrors of retrace's REST surface. Every property name here is a `json:`
// tag transcribed from the Go that serves it — retrace/diff/summary.go,
// retrace/diff/wire.go, retrace/diff/hop.go, retrace/runs/manifest.go and
// retrace/serve/queue.go. If a Go type gains a field, its tag is the TS
// property name.
//
// PRESENCE, and it is not uniform across the surface — this is R-W:
//
//   - On `Summary`, every array-valued field really is always an array:
//     `checkpoints`, `sections`, `unexpectedStatuses`, `conformance`,
//     `budgets`, `quarantined` and `gates` all carry a bare `json:"…"` tag
//     and Task 10 initialises every one of them. `summary.budgets.map(...)`
//     is safe on a flow that produced no gates at all, which is a real case:
//     "no evidence, no gate" applies to all four planes.
//   - On `Item` it was NOT true, and the two types use the field name
//     `gates` on the same REST surface. `Item.Gates` carried `omitempty`, so
//     a HEALTHY row omitted the key and `item.gates.length` threw
//     synchronously inside the first render of the first screen. That is
//     fixed on the Go side (retrace/serve/queue.go, pinned by
//     TestAPassingItemSerialisesGatesAsAnEmptyArray) rather than worked
//     around here, so the invariant now holds across the whole surface
//     instead of carving out an exception no consumer could be expected to
//     remember.
//   - `Item.refRunId` is the one that stays optional, and correctly: it is a
//     string, and "there is no reference run" is a real state that its
//     absence expresses honestly. The row handles it.

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

export type Verdict = 'ok' | 'suspect' | 'degraded' | 'broken' | 'failed';
export interface TrustReason {
  code: string;
  status: Verdict;
  detail: string;
  hint?: string;
}
export interface Gap {
  from: string;
  to: string;
  seconds: number;
}
export interface CaptureTrust {
  status: Verdict;
  reasons?: TrustReason[];
  gaps?: Gap[];
  summary: string;
  hint?: string;
}

export interface Rect {
  x: number;
  y: number;
  width: number;
  height: number;
}
export interface CheckpointVerdict {
  name: string;
  verdict: 'ok' | 'changed' | 'missing' | 'added' | 'unreadable';
  diffPct: number;
  diffPctFine: number;
  numDiff: number;
  mismatch?: boolean;
  overlap?: {
    width: number;
    height: number;
    diffPct: number;
    diffPctFine: number;
    numDiff: number;
    paddingPct: number;
  };
  trimmed?: { a?: Rect; b?: Rect };
  images: { a?: string; b?: string; diff?: string; overlay?: string };
}

export interface FieldDiff {
  scope: string;
  path: string;
  type: 'changed' | 'added' | 'removed';
  a?: unknown;
  b?: unknown;
  matcher?: string;
  glob?: string;
}
export interface HeaderDiff {
  scope: string;
  name: string;
  type: 'changed' | 'added' | 'removed' | 'tolerated' | 'violation';
  a?: string;
  b?: string;
  matcher?: string;
}
export interface Entry {
  method: string;
  normalizedPath: string;
  seqA: number;
  seqB: number;
  posA: number;
  posB: number;
  groupA?: string;
  groupB?: string;
  moved: boolean;
  truncated: boolean;
  classes: string[];
  statusChange?: { a: number; b: number };
  bodyDiff: FieldDiff[];
  bodyTolerated: FieldDiff[];
  bodyViolations: FieldDiff[];
  bodyIgnored: FieldDiff[];
  orderingChanges: FieldDiff[];
  headerDiff: HeaderDiff[];
}
export interface Section {
  name: string | null;
  entries: Entry[];
  counts: Record<string, number>;
}

export interface Counts {
  checkpoints: number;
  pixelChanged: number;
  wirePaired: number;
  wireChanged: number;
  wireMoved: number;
  wireMissing: number;
  wireExtra: number;
  violations: number;
  hopNew: number;
  hopGone: number;
  unexpectedStatuses: number;
  conformance: number;
}

export interface Item {
  app: string;
  flow: string;
  verdict: 'pass' | 'changed' | 'failed';
  score: number;
  runId: string;
  /** Absent when no reference run resolved — see the presence note above. */
  refRunId?: string;
  counts: Counts;
  capture: { a: CaptureTrust; b: CaptureTrust };
  gates: string[];
}

export interface Call {
  method: string;
  path: string;
  seq: number;
  status: number;
  group?: string;
  tolerated?: { id: string; reason: string };
}
export interface GroupNames {
  a: string[];
  b: string[];
}

export interface Manifest {
  schema: string;
  app: string;
  flow: string;
  runId: string;
  mode: 'ensemble' | 'standalone';
  git: { sha: string; branch: string; dirty: boolean };
  startedAt: string;
  finishedAt: string;
  checkpoints: { name: string; file: string; width: number; height: number; trim?: boolean }[];
  groups?: { name: string; startedAt: string; endedAt: string; quiet?: boolean }[];
  capture: CaptureTrust;
  wire: { calls: number };
  hops?: { calls: number };
  test: { command: string; exitCode: number; durationMs: number };
  env: { go: string; platform: string; retrace: string };
}
export interface RunRef {
  runId: string;
  kind: 'bundle' | 'run' | 'none';
  dir: string;
  manifest: Manifest;
}

export interface Route {
  to: string;
  method: string;
  path: string;
  via?: string[];
}
export interface ServiceCount {
  service: string;
  a: number;
  b: number;
  deviates: boolean;
}
export interface RouteFailure {
  method: string;
  path: string;
  expectedStatus: number;
  actualStatus: number;
  reason: 'missing' | 'wrong-status';
}
export interface StatusFinding {
  seq: number;
  method: string;
  path: string;
  status: number;
}
export interface HopDiff {
  serviceCounts: ServiceCount[];
  newErrors?: StatusFinding[];
  goneErrors?: StatusFinding[];
  newRoutes: Route[];
  goneRoutes: Route[];
  requiredFailures?: RouteFailure[];
  hopRequireConfigured: boolean;
}

export interface PerfResult {
  status: 'ok' | 'over' | 'unset';
  measuredMs: number;
  budgetMs: number;
}
export interface ConformanceFinding {
  method: string;
  path: string;
  status: number;
  kind:
    | 'unknown-path'
    | 'unknown-method'
    | 'undocumented-status'
    | 'missing-required-field'
    | 'unchecked';
  detail: string;
}
export interface Gate {
  plane: 'pixel' | 'wire' | 'hop' | 'perf';
  threshold: number;
  observed: number;
  failed: boolean;
}
export interface Quarantine {
  side: 'a' | 'b';
  reason: string;
}

export interface Summary {
  schema: string;
  app: string;
  flow: string;
  /**
   * Four values, not three. "quarantined" is the "could not evaluate" state
   * a signal-killed or untrustworthy run takes, and it is the one an
   * exhaustive switch most needs to handle, because it is the case where
   * every other field is empty ON PURPOSE.
   */
  verdict: 'pass' | 'changed' | 'failed' | 'quarantined';
  a: RunRef;
  b: RunRef;
  quarantined: Quarantine[];
  checkpoints: CheckpointVerdict[];
  wire: { paired: Entry[]; missing: Call[]; extra: Call[]; groups?: GroupNames };
  sections: Section[];
  hops: HopDiff;
  unexpectedStatuses: StatusFinding[];
  perf: PerfResult;
  conformance: ConformanceFinding[];
  openApiConfigured: boolean;
  capture: { a: CaptureTrust; b: CaptureTrust };
  counts: Counts;
  gates: string[];
  budgets: Gate[];
}

/**
 * WHY the review queue has nothing in it — R-V, and it is a value on the
 * wire, not something this app re-derives from `items.length === 0`.
 *
 * `EmptyAllClear` requires positive evidence on the server (at least one
 * flow, compared, every one scoring zero); `no-runs` is a setup step nobody
 * has done. An empty list renders them identically, and the reassuring
 * reading is the one a reviewer defaults to — they conclude the project is
 * clean on the strength of never having recorded anything.
 *
 * A string UNION, not `string`: an unhandled fourth value must be a type
 * error, not a blank pane.
 */
export type EmptyReason = '' | 'no-runs' | 'all-clear';

export interface QueueResponse {
  items: Item[];
  empty: EmptyReason;
}

export interface ItemResponse {
  summary: Summary;
}
