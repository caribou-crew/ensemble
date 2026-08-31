// Wire types rendered by this package's diff-viewer components
// (ShotCompare, WireDiffTable, HopDeltaList, CaptureBanner). Moved here from
// retrace-ui's api/types.ts (see openspec/changes/retrace-ci-sync/design.md
// D5) so both retrace-ui and ensemble-ui's RetraceView render the same wire
// shapes through the same components rather than a second implementation.
//
// Every property name here is a `json:` tag transcribed from the Go that
// serves it — retrace/diff/summary.go, retrace/diff/wire.go,
// retrace/diff/hop.go. retrace-ui/src/api/types.ts re-exports everything
// below so its own REST-surface mirrors (Item, Summary, Manifest, …) keep a
// single import path for existing call sites.

/**
 * A capture-trust verdict — core/trace.Verdict — and the EMPTY STRING is a
 * member, not an omission.
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
  /**
   * The candidate ("b") side's capture moment — an ISO timestamp, or Go's
   * zero-time string ("0001-01-01T00:00:00Z") when this run predates
   * runs.Checkpoint.At or this checkpoint has no candidate shot at all
   * (verdict "missing"). Never omitted on the wire (retrace/diff/summary.go's
   * `At` has no `omitempty`), so callers check for the zero value rather
   * than for absence — see ShotCompare's hasTimestamp.
   */
  at: string;
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
  /**
   * 'ignored' appears only in `Entry.headerIgnored`, never in
   * `Entry.headerDiff` — the two lists are disjoint by construction and
   * conflating them turns a silenced header into a finding.
   */
  type: 'changed' | 'added' | 'removed' | 'tolerated' | 'violation' | 'ignored';
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
  /**
   * Headers an `ignore` matcher silenced. Kept out of `headerDiff` because
   * everything in that array is a finding — the Go side's classify() and
   * triage both read it that way — and an ignored header is the opposite of
   * one. Render it as suppressed context, never as a difference.
   */
  headerIgnored: HeaderDiff[];
}
export interface Section {
  /**
   * NEVER null, and that is the whole finding.
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
