// TS mirrors of retrace's REST surface. Every property name here is a
// `json:` tag transcribed from the Go that serves it. The wire types
// themselves live in @ensemble/design-system — diffTypes.ts for the
// diff-viewer shapes (Verdict, CaptureTrust, Section, ...) alongside the
// components that render them, retraceTypes.ts for the rest (Item,
// Summary, Manifest, RunRow, ...) alongside the client both retrace-ui and
// ensemble-ui's Retrace tab share — so this file is a thin re-export kept
// only so every existing `from './api/types'` import site in this app
// keeps working unchanged.

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

export type {
  Counts,
  Source,
  Item,
  Call,
  GroupNames,
  RunCounts,
  Manifest,
  RunRef,
  PerfResult,
  ConformanceFinding,
  Gate,
  Quarantine,
  Summary,
  Suppression,
  TriageSignals,
  Triage,
  EmptyReason,
  QueueResponse,
  ItemResponse,
  RunRow,
  RunsResponse,
  SyncCandidate,
  SyncCandidatesResponse,
  SyncSelection,
  SyncResult,
  Evidence,
} from '@ensemble/design-system/retraceTypes';
