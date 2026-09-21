/** Portable runner reports for `retrace suite import --file report.json`.
 * Inventory validation and pass/fail aggregation belong to the Go importer.
 * This reporter never infers success from an exit code or absent evidence. */
export type SuitePlatform = 'web' | 'ios' | 'android';
export type SuitePlaneState = 'pass' | 'failed' | 'incomplete' | 'not-run' | 'not-applicable';
export interface SuiteIdentity {
  suiteId: string;
  suiteVersion: string;
  attemptId: string;
  platform: SuitePlatform;
  git: { sha: string; branch: string; dirty: boolean };
  /** Required for dirty builds: a stable digest/ID of the actual source snapshot. */
  workspaceId?: string;
  baselineId: string;
  policyId: string;
}
export interface SuiteResult {
  flowId: string;
  planes: { functional: SuitePlaneState; wire: SuitePlaneState; visual: SuitePlaneState };
  reason?: string;
  evidence?: { app: string; flow: string; runId: string; pairId?: string };
  /** Images to attach (typically a native flow's final screen). `file` is relative to the
   * report file; `retrace suite import` copies the bytes into content-addressed storage. */
  screens?: Array<{ label: string; file: string }>;
  /** Runner's own statement about wire evidence that is absent or not compared. Never a pass. */
  wireNote?: string;
}
export interface SuiteAttempt extends SuiteIdentity {
  schema: 'retrace/suite-attempt/1';
  startedAt: string;
  finishedAt: string;
  results: SuiteResult[];
}
const states = new Set(['pass', 'failed', 'incomplete', 'not-run', 'not-applicable']);
function component(value: string, name: string): void {
  if (typeof value !== 'string' || !/^[A-Za-z0-9][A-Za-z0-9._-]*$/.test(value)) {
    throw new Error(`retrace suite: invalid ${name}`);
  }
}
function timestamp(now: () => Date): string {
  const date = now();
  if (!(date instanceof Date) || !Number.isFinite(date.getTime())) throw new Error('retrace suite: invalid clock');
  return date.toISOString();
}

/** Start this before executing the suite, using the source/build identity the
 * runner actually delivers. record() is explicit per-flow evidence; omitted
 * expected flows remain not-run in the dashboard. finish() seals this builder.
 * Write the returned JSON to a new file and import via the CLI. Never pass
 * secrets or raw network payloads in reason; use a redacted explanation. */
export function createSuiteAttempt(identity: SuiteIdentity, now: () => Date = () => new Date()) {
  component(identity.suiteId, 'suiteId');
  component(identity.attemptId, 'attemptId');
  for (const name of ['suiteVersion', 'baselineId', 'policyId'] as const) {
    if (typeof identity[name] !== 'string' || !identity[name].trim()) throw new Error(`retrace suite: missing ${name}`);
  }
  if (!['web', 'ios', 'android'].includes(identity.platform)) throw new Error('retrace suite: invalid platform');
  if (!identity.git || !/^(?:[a-fA-F0-9]{40}|[a-fA-F0-9]{64})$/.test(identity.git.sha) || typeof identity.git.dirty !== 'boolean') {
    throw new Error('retrace suite: full git sha and explicit dirty flag required');
  }
  if (typeof identity.git.branch !== 'string') throw new Error('retrace suite: git.branch must be a string');
  if (identity.git.dirty && !identity.workspaceId?.trim()) throw new Error('retrace suite: dirty source requires workspaceId');
  const metadata = structuredClone(identity);
  const startedAt = timestamp(now);
  const results: SuiteResult[] = [];
  const seen = new Set<string>();
  let finished = false;
  const ensureOpen = () => { if (finished) throw new Error('retrace suite: attempt already finished'); };
  return {
    record(result: SuiteResult): void {
      ensureOpen();
      component(result.flowId, 'flowId');
      if (seen.has(result.flowId)) throw new Error(`retrace suite: duplicate flow ${result.flowId}`);
      for (const plane of ['functional', 'wire', 'visual'] as const) {
        if (!states.has(result.planes?.[plane])) throw new Error(`retrace suite: explicit ${plane} plane state required`);
      }
      if (result.evidence) {
        for (const key of ['app', 'flow', 'runId'] as const) component(result.evidence[key], key);
        if (result.evidence.pairId !== undefined) component(result.evidence.pairId, 'pairId');
      }
      if (result.screens !== undefined) {
        if (!Array.isArray(result.screens) || result.screens.length > 12) throw new Error('retrace suite: at most 12 screens per result');
        const labels = new Set<string>();
        for (const screen of result.screens) {
          const label = screen?.label;
          if (typeof label !== 'string' || !label || label !== label.trim() || label.length > 80) throw new Error('retrace suite: invalid screen label');
          if (labels.has(label)) throw new Error(`retrace suite: duplicate screen label ${label}`);
          labels.add(label);
          const file = screen.file;
          if (typeof file !== 'string' || !file || file.startsWith('/') || /^[A-Za-z]:/.test(file) || file.split(/[\\/]/).includes('..')) {
            throw new Error('retrace suite: screen file must be a relative path inside the report directory');
          }
        }
      }
      if (result.wireNote !== undefined && (typeof result.wireNote !== 'string' || !result.wireNote || result.wireNote !== result.wireNote.trim() || result.wireNote.length > 400)) {
        throw new Error('retrace suite: wireNote must be 1-400 characters without surrounding space');
      }
      seen.add(result.flowId);
      results.push(structuredClone(result));
    },
    finish(): SuiteAttempt {
      ensureOpen();
      const finishedAt = timestamp(now);
      if (finishedAt < startedAt) throw new Error('retrace suite: finish before start');
      finished = true;
      return { ...metadata, schema: 'retrace/suite-attempt/1', startedAt, finishedAt, results: structuredClone(results) };
    },
  };
}
