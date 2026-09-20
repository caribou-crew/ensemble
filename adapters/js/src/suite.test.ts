import { describe, it, expect } from 'vitest';
import { createSuiteAttempt } from './suite.js';

const identity = {
  suiteId: 'legacy-to-taxi', suiteVersion: 'v1', attemptId: 'job-27', platform: 'ios' as const,
  git: { sha: 'a'.repeat(40), branch: 'main', dirty: false }, baselineId: 'legacy-v1', policyId: 'strict-v1',
};
const passed = { functional: 'pass', wire: 'pass', visual: 'pass' } as const;
const clock = () => new Date('2026-09-20T00:00:00Z');

describe('suite attempt reporter', () => {
  it('emits the portable CLI-import contract without inferring a result for missing flows', () => {
    const report = createSuiteAttempt(identity, clock);
    report.record({ flowId: 'login', planes: passed });
    expect(report.finish()).toEqual({ ...identity, schema: 'retrace/suite-attempt/1', startedAt: clock().toISOString(), finishedAt: clock().toISOString(), results: [{ flowId: 'login', planes: passed }] });
  });
  it.each([undefined, null, 123, false, {}])('rejects non-string git.branch %s before execution', branch => {
    expect(() => createSuiteAttempt({ ...identity, git: { ...identity.git, branch } } as unknown as Parameters<typeof createSuiteAttempt>[0], clock)).toThrow(/branch/);
  });
  it('accepts an empty branch for detached HEAD', () => {
    expect(createSuiteAttempt({ ...identity, git: { ...identity.git, branch: '' } }, clock).finish().git.branch).toBe('');
  });
  it('requires a snapshot identity for dirty sources', () => {
    expect(() => createSuiteAttempt({ ...identity, git: { ...identity.git, dirty: true } }, clock)).toThrow(/workspaceId/);
  });
  it('retains explicit failures and incomplete visual evidence', () => {
    const report = createSuiteAttempt(identity, clock);
    report.record({ flowId: 'login', planes: { functional: 'pass', wire: 'failed', visual: 'incomplete' }, reason: 'Unexpected request' });
    expect(report.finish().results[0].planes).toEqual({ functional: 'pass', wire: 'failed', visual: 'incomplete' });
  });
  it('rejects omitted planes and duplicate flow results', () => {
    const report = createSuiteAttempt(identity, clock);
    expect(() => report.record({ flowId: 'login', planes: { functional: 'pass' } as typeof passed })).toThrow(/plane/);
    report.record({ flowId: 'login', planes: passed });
    expect(() => report.record({ flowId: 'login', planes: passed })).toThrow(/duplicate/);
  });
  it('snapshots caller objects and closes permanently after finish', () => {
    const metadata = structuredClone(identity);
    const result = { flowId: 'login', planes: { ...passed }, evidence: { app: 'taxi', flow: 'login', runId: 'run1' } };
    const report = createSuiteAttempt(metadata, clock);
    report.record(result);
    metadata.baselineId = 'changed'; result.evidence.runId = 'changed';
    const finished = report.finish();
    expect(finished.baselineId).toBe('legacy-v1');
    expect(finished.results[0].evidence?.runId).toBe('run1');
    expect(() => report.record(result)).toThrow(/finished/);
    expect(() => report.finish()).toThrow(/finished/);
  });
  it('rejects backwards or invalid clocks', () => {
    const times = [new Date('2026-09-20T00:00:01Z'), clock()];
    const report = createSuiteAttempt(identity, () => times.shift()!);
    expect(() => report.finish()).toThrow(/before/);
    expect(() => createSuiteAttempt(identity, () => new Date('bad'))).toThrow(/clock/);
  });
  it('does not turn an empty run into a passing flow', () => {
    expect(createSuiteAttempt(identity, clock).finish().results).toEqual([]);
  });
});
