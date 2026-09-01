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

// The wire types ShotCompare, WireDiffTable, HopDeltaList and CaptureBanner
// render — Verdict, TrustReason, Gap, CaptureTrust, Rect, CheckpointVerdict,
// FieldDiff, HeaderDiff, Entry, Section, Route, ServiceCount, RouteFailure,
// StatusFinding, HopDiff — moved to @ensemble/design-system/diffTypes
// alongside those components (design.md D5), so retrace-ui and ensemble-ui's
// RetraceView share one definition instead of two. Re-exported from here so
// every existing `from './api/types'` import site in this app keeps working
// unchanged.
export type {
  Verdict,
  TrustReason,
  Gap,
  CaptureTrust,
  Rect,
  CheckpointVerdict,
  FieldDiff,
  HeaderDiff,
  Entry,
  Section,
  Route,
  ServiceCount,
  RouteFailure,
  StatusFinding,
  HopDiff,
} from '@ensemble/design-system/diffTypes';
import type {
  CaptureTrust,
  CheckpointVerdict,
  Entry,
  HopDiff,
  Section,
  StatusFinding,
} from '@ensemble/design-system/diffTypes';

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
  /**
   * Where this run came from. ABSENT is the encoding of "recorded locally"
   * — `runs.Source` carries `omitempty` and is only ever written by
   * `retrace sync`, so a row with no `source` is a local run and a row with
   * one is a CI run pulled down from a workflow. Do NOT default it to a
   * placeholder object, which would make every local row look like a CI
   * sync.
   */
  source?: Source;
}

/**
 * `runs.Source` — the CI provenance `retrace sync` stamps onto a pulled run.
 * Its very PRESENCE means "this came from CI"; a locally recorded run has no
 * source.json and so no `source` field on its Item at all (see Item.source).
 */
export interface Source {
  schema: string;
  /** Always "ci" today: source.json is only written by `retrace sync`. */
  kind: string;
  /** The GitHub Actions workflow name the run was recorded under. */
  workflow: string;
  /** The workflow run's web URL, for jumping to the CI log. */
  runUrl: string;
  /** The commit the run was recorded against. */
  sha: string;
  /** The branch the workflow ran against, e.g. "main". */
  headBranch?: string;
  /** What triggered the run, e.g. "push" or "schedule". */
  event?: string;
  /** The GitHub login who triggered the run. */
  actor?: string;
  /** When `retrace sync` merged this run onto local disk. */
  syncedAt: string;
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
  checkpoints: { name: string; file: string; width: number; height: number; trim?: boolean; at: string }[];
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
  /**
   * The checkpoint this row's `threshold` came from, set only when a
   * per-checkpoint budget (`gates: {pixel: {checkpoints: {cart: 8}}}`)
   * decided it. Absent means the plane's own `budget_pct` applied to
   * everything, which is every run in a project that configures no
   * overrides. Show it beside the plane rather than in place of it — a run
   * where one screen is allowed 8% and the rest 1.5% otherwise displays a
   * single threshold that is true of neither.
   */
  checkpoint?: string;
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
  /**
   * Every tolerance that actually silenced a difference in this run, with
   * how many times it fired, loudest first. Rows that suppressed nothing
   * are absent, so an empty array means the verdict was earned rather than
   * configured — which is the one thing a clean report cannot otherwise
   * say about itself.
   */
  suppressions: Suppression[];
  triage: Triage;
}

export interface Suppression {
  plane: 'header' | 'body';
  /** Header name, or the body field-path glob that matched. */
  target: string;
  /**
   * Where the tolerance came from. `builtin` is one retrace ships with and
   * nobody here asked for (see config.BuiltinWireRules) — worth showing
   * differently from the two a person in this project chose.
   *
   * Deliberately a union: unlike a triage label, these three are the
   * complete set the engine can emit and are not configurable.
   */
  source: 'wire_rule' | 'wire_ignore' | 'builtin';
  matcher: string;
  count: number;
  /**
   * The `why:` the config gave this tolerance, verbatim. Optional because
   * `require_why` is opt-in, so an un-explained tolerance is legal — and the
   * field is omitted rather than sent as a placeholder, so absent must render
   * as absent. Do NOT substitute "no reason given" here either: that would
   * make an unexplained rule look documented, which is the exact confusion
   * the field exists to remove.
   */
  why?: string;
}

/**
 * The five moved/same signals the triage table classifies on. `true` means
 * that plane moved.
 *
 * Carried alongside the label rather than re-derived from the rest of the
 * Summary, and three of the five could not be re-derived correctly anyway:
 * `hop` folds new routes, gone routes and per-service count deviation into
 * one bit; `spec` excludes "unchecked" conformance findings, which are the
 * absence of evidence rather than drift; and `capture` is true for a
 * quarantine whose own capture verdict is "ok" — a signal-killed run.
 */
export interface TriageSignals {
  pixel: boolean;
  wire: boolean;
  hop: boolean;
  spec: boolean;
  capture: boolean;
}

/**
 * Whose problem this is — the one question the four planes leave to the
 * reader.
 *
 * `label` is one of the built-ins (`harness`, `client-behavior`, `stack`,
 * `contract-drift`, `client-ui`, `none`, `unclassified`) OR any string a
 * project's own `triage:` rule chose, so do NOT type it as a union: a
 * project label is an ordinary configuration, not a bad value, and an
 * exhaustive switch over the built-ins would silently drop it.
 *
 * `none` and `unclassified` are NOT synonyms. `none` means no signal moved
 * and the verdict is clean. `unclassified` means no signal moved and the run
 * still is not a pass — a perf budget, an unexpected status, a hopRequire
 * route or an unevaluated gate, none of which the five signals cover.
 * Rendering the second as "nothing to see" is the reassuring-value trap on
 * the run that most needs reading; `gates` is where its reason lives.
 */
export interface Triage {
  label: string;
  /** The rule that matched: a built-in name, or the project rule's own. */
  rule: string;
  signals: TriageSignals;
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

/**
 * One run of a surface in the runs-list drill-down — the TS mirror of
 * serve.RunRow. Lighter than Item: Item is "the one run worth reviewing for
 * this surface" (always the newest), a RunRow is "one of the surface's
 * runs, enough to pick which to open". `when` is an ISO timestamp string
 * (Go's time.Time marshals to RFC3339); `source` follows the same presence
 * contract as Item.source (absent == local, present == a CI sync).
 */
export interface RunRow {
  runId: string;
  verdict: 'pass' | 'changed' | 'failed' | 'quarantined';
  when: string;
  source?: Source;
  counts: Counts;
  gates: string[];
}

export interface RunsResponse {
  runs: RunRow[];
}

// --- sync (discover -> filter -> select -> pull): retrace/serve/sync.go's
// GET /api/sync/candidates and POST /api/sync, mirroring sync.Candidate,
// sync.Selection and sync.Result field-for-field.

export interface SyncCandidate {
  repo: string;
  databaseId: number;
  workflowName: string;
  headBranch: string;
  actor: string;
  event: string;
  status: string;
  conclusion: string;
  createdAt: string;
  url: string;
  hasArtifacts: boolean;
  /**
   * Every "app/flow/run-id" already pulled from this CI run
   * (runs.SourcesByURL's reverse index, joined on RunURL server-side).
   * Never null — an empty array means "not pulled yet", the same
   * never-null contract every other array on this response carries. A
   * click-to-view sync panel uses this to open a candidate directly
   * instead of re-pulling it, and to know whether the run produced one
   * flow or several (multiple entries here means an inline chooser).
   */
  localRuns: string[];
}

export interface SyncCandidatesResponse {
  candidates: SyncCandidate[];
}

export interface SyncSelection {
  repo: string;
  databaseId: number;
}

export interface SyncResult {
  synced: string[];
  skipped: { artifact: string; reason: string }[];
}

/**
 * GET /api/evidence/{app}/{flow}'s body — retrace/serve/evidence.go's
 * Evidence, what's available to view for the candidate ("b") run, never the
 * files themselves. `videos` is never null on the wire — always `[]` for
 * "none attached" — because it attaches to a run AFTER `retrace run`/sync
 * finishes and is never part of Summary.
 */
export interface Evidence {
  videos: string[];
  hasReport: boolean;
}
