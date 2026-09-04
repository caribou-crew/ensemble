// TS mirrors of retrace/serve's REST surface — the wire shapes shared by
// retrace-ui (standalone `retrace serve`) and ensemble-ui's Retrace tab
// (embedded via ensemble/server, importing retrace/serve directly). Both
// apps hit structurally identical JSON (retrace/diff/summary.go,
// retrace/runs/manifest.go, retrace/serve/queue.go) under different route
// prefixes (`/api/...` vs `/api/retrace/...`) — see retraceClient.ts for the
// base-path parameterization. One type definition here replaces what used
// to be two independently hand-ported copies.
//
// The diff-viewer WIRE types (Verdict, CaptureTrust, CheckpointVerdict,
// Section, HopDiff, ...) live in ./diffTypes instead, alongside the
// components that render them (ShotCompare, WireDiffTable, HopDeltaList,
// CaptureBanner) — import those from '@ensemble/design-system/diffTypes'
// directly.

import type {
  CaptureTrust,
  CheckpointVerdict,
  Entry,
  HopDiff,
  Section,
  StatusFinding,
} from './diffTypes';

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

/**
 * `runs.Source` — the CI provenance `retrace sync` stamps onto a pulled run.
 * Its very PRESENCE means "this came from CI"; a locally recorded run has no
 * source.json and so no `source` field on its Item/RunRow at all.
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

export interface Item {
  app: string;
  flow: string;
  /**
   * Four values, not three: `quarantined` is "could not evaluate" — see
   * retraceTone.ts's verdictTone, which is TOTAL over all four and the only
   * place a wire verdict is mapped to a colour.
   */
  verdict: 'pass' | 'changed' | 'failed' | 'quarantined';
  score: number;
  runId: string;
  /** Absent when no reference run resolved. */
  refRunId?: string;
  /** When the reviewed run finished — see RunRow.when's own doc comment for
   * the fallback chain and the zero-time case. */
  when: string;
  counts: Counts;
  capture: { a: CaptureTrust; b: CaptureTrust };
  gates: string[];
  /** ABSENT is the encoding of "recorded locally" — see Source's own doc
   * comment. Do NOT default it to a placeholder object. */
  source?: Source;
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
  groups: { name: string; startedAt: string; endedAt: string; quiet?: boolean }[];
  capture: CaptureTrust;
  wire: RunCounts;
  /** nil in standalone mode — the only spelling of "the hop plane was not
   * recorded". */
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
  kind: 'unknown-path' | 'unknown-method' | 'undocumented-status' | 'missing-required-field' | 'unchecked';
  detail: string;
}
export interface Gate {
  plane: 'pixel' | 'wire' | 'hop' | 'perf';
  threshold: number;
  observed: number;
  failed: boolean;
  /** The checkpoint this row's threshold came from, set only for a
   * per-checkpoint budget override. Absent means the plane's own
   * `budget_pct` applied. */
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
  verdict: 'pass' | 'changed' | 'failed' | 'quarantined';
  a: RunRef;
  b: RunRef;
  quarantined: Quarantine[];
  /** Set when a geometry mismatch was downgraded to wire-only (the pixel
   * plane was skipped but wire/hop still produced a verdict). Explains the
   * empty shots plane without failing the run. Absent in strict mode. */
  geometryNote?: string;
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
  /** The planes `gates:` configures that this comparison could not measure
   * — gated, and no evidence to gate against. Not derived client-side. */
  unmeasuredGates: string[];
  suppressions: Suppression[];
  triage: Triage;
}

export interface Suppression {
  plane: 'header' | 'body';
  target: string;
  source: 'wire_rule' | 'wire_ignore' | 'builtin';
  matcher: string;
  count: number;
  why?: string;
}

export interface TriageSignals {
  pixel: boolean;
  wire: boolean;
  hop: boolean;
  spec: boolean;
  capture: boolean;
}

/**
 * `label` is one of the built-ins OR any string a project's own `triage:`
 * rule chose — never typed as a union, so a project label is not dropped by
 * an exhaustive switch.
 */
export interface Triage {
  label: string;
  rule: string;
  signals: TriageSignals;
}

/**
 * WHY the review queue has nothing in it — a value on the wire, not
 * something re-derived from `items.length === 0`. A string UNION so an
 * unhandled fourth value is a type error, not a blank pane.
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
 * One run of a surface in the runs-list drill-down — lighter than Item:
 * Item is "the one run worth reviewing for this surface" (always the
 * newest), a RunRow is "one of the surface's runs, enough to pick which to
 * open".
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

// --- sync (discover -> filter -> select -> pull) ---

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
  /** Every "app/flow/run-id" already pulled from this CI run. Never null —
   * an empty array means "not pulled yet". */
  localRuns: string[];
}

export interface SyncCandidatesResponse {
  candidates: SyncCandidate[];
}

/** One branch ListBranches (Go) found: its name and its most recent
 * qualifying run's timestamp and triggering event. */
export interface BranchCandidate {
  name: string;
  lastRunAt: string;
  lastEvent: string;
}

export interface SyncBranchesResponse {
  branches: BranchCandidate[];
}

/** GET {basePath}/sync/config — the repo.yaml sync defaults the standalone
 * `retrace serve` exposes so the Browse-&-sync panel prefills the repo and
 * filters. Empty `repo` means no configured default (the panel asks). */
export interface SyncConfigResponse {
  repo: string;
  workflows?: string[];
  branch?: string;
  actor?: string;
  event?: string;
  status?: string;
  since?: string;
}

export interface SyncSelection {
  repo: string;
  databaseId: number;
}

export interface SyncResult {
  synced: string[];
  skipped: { artifact: string; reason: string }[];
}

/** What's available to view for a candidate run, never the files
 * themselves. `videos` is never null on the wire. */
export interface Evidence {
  videos: string[];
  hasReport: boolean;
}
