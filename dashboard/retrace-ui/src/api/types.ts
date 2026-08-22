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
//     `budgets`, `unmeasuredGates`, `quarantined` and `gates` all carry a
//     bare `json:"…"` tag and Task 10 initialises every one of them.
//     `summary.budgets.map(...)` is safe on a flow that produced no gates at
//     all, which is a real case: "no evidence, no gate" applies to all four
//     planes — and `unmeasuredGates` is how such a flow says so out loud.
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
//
// R-X: every type below has been walked FROM THE GO STRUCT that produces it
// — retrace/diff, retrace/runs, retrace/serve — field by field against its
// `json:` tag, and the table is in task-15-report.md. The direction matters:
// an audit driven from THIS file cannot see a field the Go sends and this
// file never mentions, which is the shape that produced most of the bugs
// (accept's whole `bundle`, reject's `warning`, `wire.recorded`). The three
// shapes it hunts are (1) a union missing a member the Go can emit, (2) a
// field typed required whose Go tag carries `omitempty`, (3) a field the Go
// sends that is absent here.
//
// Where this file is DELIBERATELY wider than the Go — `FieldDiff.a`/`b`,
// `HeaderDiff.a`/`b`, `HopDiff.newErrors`/`goneErrors`/`requiredFailures` and
// `Route.via` are typed optional where the Go tag is bare — that costs a
// redundant guard and cannot mislead, so it is left alone. Narrowing is the
// direction that hurts.

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

/**
 * A capture-trust verdict — core/trace.Verdict — and the EMPTY STRING is a
 * member, not an omission (R-X, shape 1).
 *
 * trace.Verdict is a Go string type whose zero value is "", and no type on
 * the Go side prevents a struct literal from carrying it. `serve.brokenItem`
 * did exactly that until N-3: it folded a hand-built zero `diff.Summary` into
 * a queue row for any flow that could not be diffed, so
 * `{"status":"","summary":""}` reached this app for exactly the rows that most
 * need a human.
 *
 * NO PRODUCTION PATH EMITS IT TODAY. `ReadManifest` refuses an empty capture
 * status, `diff.Build` sets `Summary.Capture` from both manifests before even
 * the quarantine exits, and `brokenItem` now sends `failed` plus a
 * `capture-not-assessed` reason. This member is therefore not describing a
 * live case; it is what makes the tone table TOTAL over the Go type's actual
 * domain, so the next `serve` construction path that forgets the field is a
 * compile error here rather than a grey badge at a reviewer's desk.
 *
 * "" ranks with the worst, never with `ok`: it is "nobody assessed this",
 * which is the state global-constraints.md's zero-value rule exists for.
 */
export type Verdict = '' | 'ok' | 'suspect' | 'degraded' | 'broken' | 'failed';
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
  /**
   * NEVER null, and that is the whole finding (R-X, shape 1 and 3 at once).
   *
   * `diff.Section.Name` is a Go `string` with a bare tag, and the unnamed
   * section is built as `buildSection("", entries)` — order.go:203 and :221.
   * It is `""` on the wire, always. A `string | null` here made
   * `section.name ?? 'before any marker'` dead code (`'' ?? x` is `''`), so
   * every flow that has not adopted group markers — which is the ordinary
   * case, since BuildSections returns a single `""`-named section when a run
   * declared no markers — rendered its whole wire plane under a blank header.
   */
  name: string;
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
  /**
   * The same four values as `Summary.verdict` — `Item.Verdict` is copied
   * from it verbatim by `serve.itemOf`, so a three-value union here was a
   * narrowing of the type one field over on the same REST surface.
   *
   * `quarantined` is the expensive one to drop: `ScoreOf` scores it 1000,
   * deliberately the top of the queue, and a `verdictTone` with no arm for it
   * returned `undefined`, which `<Badge>` paints NEUTRAL GREY. The queue
   * sorted a row to the very top because it demands attention and then
   * painted it the colour of a non-event; colour is pre-attentive and sort
   * order is not, so colour wins.
   */
  verdict: 'pass' | 'changed' | 'failed' | 'quarantined';
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

/**
 * `runs.Counts` — one recorded plane's call count, and D4.
 *
 * `recorded` is the field that makes `calls` readable, and dropping it from
 * this mirror is what makes `wire.calls === 0` read as a clean wire plane.
 * `runs.Counts`' own doc names that as its entire reason for existing:
 * "wire recorded, none seen" and "wire never recorded" are different worlds,
 * and only `recorded: false` distinguishes them. `reason` says WHY the plane
 * was not recorded when it was not.
 *
 * Nothing in this task renders manifest counts. This is the type only,
 * corrected now because the next consumer inherits it either way and it is
 * expensive to correct later.
 */
export interface RunCounts {
  calls: number;
  recorded: boolean;
  reason?: string;
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
  // Never absent: `runs.Manifest.Groups` carries a bare tag and WriteManifest
  // defaults a nil slice to `[]Group{}`, exactly as Checkpoints does.
  groups: { name: string; startedAt: string; endedAt: string; quiet?: boolean }[];
  capture: CaptureTrust;
  wire: RunCounts;
  /** nil in standalone mode, and that nil is the ONLY spelling of "the hop
   * plane was not recorded" — see runs.Manifest.Hops. */
  hops?: RunCounts;
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
  /**
   * The planes `gates:` configures that this comparison could not measure —
   * gated, and no evidence to gate against. Mirrors diff.Summary's own
   * field; it is NOT derived here, and must not be: a consumer that
   * re-derived "gated but unmeasured" privately while the others read
   * `budgets` alone is the bug this field exists to close.
   *
   * `budgets` alone cannot say it. A plane nobody gated and a plane gated
   * with nothing to measure are the same missing row, so rendering only
   * `budgets` reports the second as the first.
   */
  unmeasuredGates: string[];
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
