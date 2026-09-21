/** Imported runner assertions. Counts represent expected flow/platform cells. */
export type SuitePlatform = 'web' | 'ios' | 'android';
export type SuitePlane = 'functional' | 'wire' | 'visual';
export type SuiteStatus = 'pass' | 'failed' | 'incomplete' | 'not-run';
export type SuitePlaneStatus = SuiteStatus | 'not-applicable';
export interface SuiteCounts { total: number; passed: number; failed: number; incomplete: number; notRun: number }
export interface SuiteEvidence { app: string; flow: string; runId: string; pairId?: string }
export interface SuiteScreen { label: string; sha256: string; media: string; bytes?: number }
export interface SuiteAttemptResult {
  attemptId: string;
  startedAt: string;
  finishedAt: string;
  planes: Record<SuitePlane, SuitePlaneStatus>;
  reason?: string;
  evidence?: SuiteEvidence;
  screens?: SuiteScreen[];
  wireNote?: string;
}
export interface SuiteFlowCell {
  platform: SuitePlatform;
  status: SuiteStatus;
  requiredPlanes: SuitePlane[];
  latest?: SuiteAttemptResult;
  history: SuiteAttemptResult[];
}
export interface SuiteFlowSummary { id: string; title: string; platforms: SuiteFlowCell[] }
export interface SuitePlatformSummary { platform: SuitePlatform; counts: SuiteCounts }
export interface SuiteFeatureSummary {
  id: string; title: string; counts: SuiteCounts;
  platforms: SuitePlatformSummary[];
  flows: SuiteFlowSummary[];
}
export interface SuiteBuild {
  id: string;
  git: { sha: string; branch: string; dirty: boolean };
  workspaceId?: string;
  baselineId: string;
  policyId: string;
  updatedAt: string;
  counts: SuiteCounts;
  platforms: SuitePlatformSummary[];
  features: SuiteFeatureSummary[];
}
export interface SuiteOverview { id: string; title: string; version: string; platforms: SuitePlatform[]; builds: SuiteBuild[] }
export interface SuitesResponse { suites: SuiteOverview[] }
export interface SuiteSelection { suiteView?: SuiteGalleryView; reviewFilter?: string; reviewSearch?: string; flowId?: string; reviewPlatform?: SuitePlatform; suiteId?: string; buildId?: string; featureId?: string; platform?: SuitePlatform }

/** Whole-build review board (GET /suites/{suite}/builds/{build}/gallery). */
export type SuiteGalleryView = 'gallery' | 'review';
export interface SuiteGalleryWireCounts { paired: number; changed: number; moved: number; missing: number; extra: number; violations: number }
/** `represented` is true only when a readable saved comparison supplied counts. */
export interface SuiteGalleryWire { represented: boolean; plane: SuitePlaneStatus; note: string; counts?: SuiteGalleryWireCounts }
export interface SuiteGalleryCheckpoint { name: string; verdict: string; diffPct: number }
export interface SuiteGalleryPair {
  app: string; flow: string; runId: string; pairId: string; available: boolean; error?: string;
  checkpoint?: string; verdict?: string; checkpoints?: SuiteGalleryCheckpoint[];
}
export interface SuiteGallerySource { buildId: string; sha: string; branch: string; dirty: boolean; baselineId: string; policyId: string; finishedAt: string }
export interface SuiteGalleryLane { platform: SuitePlatform; sources: SuiteGallerySource[] }
export interface SuiteGalleryTile {
  platform: SuitePlatform; status: SuiteStatus; planes: Record<SuitePlane, SuitePlaneStatus>; reason?: string;
  attemptId?: string; evidence?: SuiteEvidence; screens: Array<Pick<SuiteScreen, 'label' | 'sha256' | 'media'>>;
  pair?: SuiteGalleryPair; wire: SuiteGalleryWire; source?: SuiteGallerySource;
}
export interface SuiteGalleryRow { feature: { id: string; title: string }; flow: { id: string; title: string }; tiles: SuiteGalleryTile[] }
export interface SuiteGallery {
  suiteId: string; title: string; scope: 'build' | 'latest'; buildId: string; lanes: SuiteGalleryLane[]; git: { sha: string; branch: string; dirty: boolean };
  baselineId: string; policyId: string; updatedAt: string; platforms: SuitePlatform[]; rows: SuiteGalleryRow[];
}
