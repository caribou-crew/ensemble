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

// --- retrace review queue (GET /api/retrace/queue, /queue/{app}/{flow}) ---
//
// Mirrors dashboard/retrace-ui/src/api/types.ts's own Item/Summary/Manifest
// block field-for-field against the same Go structs (retrace/diff/summary.go,
// retrace/runs/manifest.go, retrace/serve/queue.go) — this app has no
// dependency on that one (a separate standalone app, not a shared package),
// so the mirror is kept here rather than imported. The diff-viewer WIRE types
// (CaptureTrust, CheckpointVerdict, Section, HopDiff, …) come from
// @ensemble/design-system/diffTypes instead, since RetraceView renders
// through the same ShotCompare/WireDiffTable/HopDeltaList/CaptureBanner
// components retrace-ui uses.
import type {
  CaptureTrust,
  CheckpointVerdict,
  Entry,
  HopDiff,
  Section,
  StatusFinding,
} from '@ensemble/design-system/diffTypes';
export type { Entry, FieldDiff, HopDiff, Section } from '@ensemble/design-system/diffTypes';

export interface RetraceCounts {
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

/** `runs.Source` — set only for a run `retrace sync` merged in from CI; a
 * local run omits the key entirely (see runs.Source's own doc comment for
 * why absence, not a "local" value, is the encoding). */
export interface RetraceSource {
  schema: string;
  kind: 'ci';
  workflow: string;
  runUrl: string;
  sha: string;
  /** Absent for a run synced before provenance fields existed. */
  headBranch?: string;
  event?: string;
  actor?: string;
  syncedAt: string;
}

export interface RetraceItem {
  app: string;
  flow: string;
  verdict: 'pass' | 'changed' | 'failed' | 'quarantined';
  score: number;
  runId: string;
  /** Absent when no reference run resolved. */
  refRunId?: string;
  counts: RetraceCounts;
  capture: { a: CaptureTrust; b: CaptureTrust };
  gates: string[];
  source?: RetraceSource;
}

export type RetraceEmptyReason = '' | 'no-runs' | 'all-clear';

export interface RetraceQueueResponse {
  items: RetraceItem[];
  empty: RetraceEmptyReason;
}

export interface RetraceRunCounts {
  calls: number;
  recorded: boolean;
  reason?: string;
}

export interface RetraceManifest {
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
  wire: RetraceRunCounts;
  hops?: RetraceRunCounts;
  test: { command: string; exitCode: number; durationMs: number };
  env: { go: string; platform: string; retrace: string };
}

export interface RetraceRunRef {
  runId: string;
  kind: 'bundle' | 'run' | 'none';
  dir: string;
  manifest: RetraceManifest;
}

export interface RetracePerfResult {
  status: 'ok' | 'over' | 'unset';
  measuredMs: number;
  budgetMs: number;
}

export interface RetraceConformanceFinding {
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

export interface RetraceGate {
  plane: 'pixel' | 'wire' | 'hop' | 'perf';
  threshold: number;
  observed: number;
  failed: boolean;
  checkpoint?: string;
}

export interface RetraceQuarantine {
  side: 'a' | 'b';
  reason: string;
}

export interface RetraceCall {
  method: string;
  path: string;
  seq: number;
  status: number;
  group?: string;
  tolerated?: { id: string; reason: string };
}

export interface RetraceGroupNames {
  a: string[];
  b: string[];
}

export interface RetraceTriageSignals {
  pixel: boolean;
  wire: boolean;
  hop: boolean;
  spec: boolean;
  capture: boolean;
}

export interface RetraceTriage {
  label: string;
  rule: string;
  signals: RetraceTriageSignals;
}

export interface RetraceSuppression {
  plane: 'header' | 'body';
  target: string;
  source: 'wire_rule' | 'wire_ignore' | 'builtin';
  matcher: string;
  count: number;
  why?: string;
}

export interface RetraceSummary {
  schema: string;
  app: string;
  flow: string;
  verdict: 'pass' | 'changed' | 'failed' | 'quarantined';
  a: RetraceRunRef;
  b: RetraceRunRef;
  quarantined: RetraceQuarantine[];
  checkpoints: CheckpointVerdict[];
  wire: { paired: Entry[]; missing: RetraceCall[]; extra: RetraceCall[]; groups?: RetraceGroupNames };
  sections: Section[];
  hops: HopDiff;
  unexpectedStatuses: StatusFinding[];
  perf: RetracePerfResult;
  conformance: RetraceConformanceFinding[];
  openApiConfigured: boolean;
  capture: { a: CaptureTrust; b: CaptureTrust };
  counts: RetraceCounts;
  gates: string[];
  budgets: RetraceGate[];
  unmeasuredGates: string[];
  suppressions: RetraceSuppression[];
  triage: RetraceTriage;
}

export interface RetraceItemResponse {
  summary: RetraceSummary;
}

/** `sync.Result` — POST /api/retrace/sync's body. `synced`/`skipped` are
 * never null on the wire (see sync.Result's own doc comment), only ever `[]`
 * for "nothing new". */
export interface RetraceSyncResult {
  synced: string[];
  skipped: { artifact: string; reason: string }[];
}

/** `sync.Candidate` — one row of GET /api/retrace/sync/candidates's
 * `candidates` array. `hasArtifacts`/`actor` both cost the server one
 * extra `gh api` call each (gh's own listing has neither), so this
 * endpoint is not meant to be polled. */
export interface RetraceCandidate {
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
   * (runs.SourcesByURL's reverse index, joined server-side on RunURL).
   * Never null — an empty array means "not pulled yet", the same
   * never-null contract this response's own `candidates` carries. A
   * click-to-view sync panel uses this to open a candidate directly
   * instead of re-pulling it, and to know whether the run produced one
   * flow or several (multiple entries here means an inline chooser).
   */
  localRuns: string[];
}

/** GET /api/retrace/sync/candidates's body. `candidates` is never null on
 * the wire, only ever `[]` for "nothing in range". */
export interface RetraceCandidatesResponse {
  candidates: RetraceCandidate[];
}

/** One candidate a caller picked from RetraceCandidatesResponse — the
 * POST /api/retrace/sync request body's `selections` array shape. */
export interface RetraceSelection {
  repo: string;
  databaseId: number;
}

/** GET /api/retrace/evidence/{app}/{flow}'s body — what's available to
 * view for the candidate run, never the files themselves. `videos` is
 * never null on the wire, only ever `[]` for "none attached". */
export interface RetraceEvidence {
  videos: string[];
  hasReport: boolean;
}
