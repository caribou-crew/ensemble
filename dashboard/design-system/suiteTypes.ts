/** Imported runner assertions. Counts represent expected flow/platform cells. */
export type SuitePlatform = 'web' | 'ios' | 'android';
export type SuitePlane = 'functional' | 'wire' | 'visual';
export type SuiteStatus = 'pass' | 'failed' | 'incomplete' | 'not-run';
export type SuitePlaneStatus = SuiteStatus | 'not-applicable';
export interface SuiteCounts { total: number; passed: number; failed: number; incomplete: number; notRun: number }
export interface SuiteEvidence { app: string; flow: string; runId: string; pairId?: string }
export interface SuiteAttemptResult {
  attemptId: string;
  startedAt: string;
  finishedAt: string;
  planes: Record<SuitePlane, SuitePlaneStatus>;
  reason?: string;
  evidence?: SuiteEvidence;
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
export interface SuiteSelection { suiteId?: string; buildId?: string; featureId?: string; platform?: SuitePlatform }
