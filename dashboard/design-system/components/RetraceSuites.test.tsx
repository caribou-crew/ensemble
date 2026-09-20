import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { SuiteAttemptResult, SuiteCounts, SuiteFlowCell, SuiteSelection, SuitesResponse } from '../suiteTypes';
import RetraceSuites from './RetraceSuites';

const counts = (passed: number, failed = 0, notRun = 0, incomplete = 0): SuiteCounts => ({ total: passed + failed + notRun + incomplete, passed, failed, notRun, incomplete });
const prior: SuiteAttemptResult = { attemptId: 'old-pass', startedAt: '2026-09-19T00:00:00Z', finishedAt: '2026-09-19T00:10:00Z', planes: { functional: 'pass', wire: 'pass', visual: 'pass' } };
const latest: SuiteAttemptResult = { ...prior, attemptId: 'retry-failed', finishedAt: '2026-09-19T00:20:00Z', planes: { functional: 'pass', wire: 'failed', visual: 'incomplete' }, reason: 'Response contract changed', evidence: { app: 'taxi', flow: 'login', runId: 'run-2', pairId: 'legacy-reference' } };
const requiredPlanes = ['functional', 'wire', 'visual'] as const;
function cell(platform: SuiteFlowCell['platform'], status: SuiteFlowCell['status'], result?: SuiteAttemptResult): SuiteFlowCell {
  return { platform, status, requiredPlanes: [...requiredPlanes], latest: result, history: result ? [result, prior] : [] };
}
function fixture(): SuitesResponse {
  return { suites: [{ id: 'migration', title: 'Legacy to Taxi', version: 'inventory-1', platforms: ['web', 'ios', 'android'], builds: [{ id: 'build-1', git: { sha: 'a'.repeat(40), branch: 'feature/taxi', dirty: true }, workspaceId: 'snapshot-9', baselineId: 'legacy-1', policyId: 'strict-1', updatedAt: '2026-09-19T00:20:00Z', counts: counts(0, 1, 1, 1), platforms: [{ platform: 'web', counts: counts(0, 1) }, { platform: 'ios', counts: counts(0, 0, 0, 1) }, { platform: 'android', counts: counts(0, 0, 1) }], features: [{ id: 'auth', title: 'Account access', counts: counts(0, 1, 1, 1), platforms: [{ platform: 'web', counts: counts(0, 1) }, { platform: 'ios', counts: counts(0, 0, 0, 1) }, { platform: 'android', counts: counts(0, 0, 1) }], flows: [{ id: 'login', title: 'Sign in', platforms: [cell('web', 'failed', latest), cell('ios', 'incomplete', { ...latest, attemptId: 'native-report', planes: { functional: 'pass', wire: 'pass', visual: 'incomplete' }, evidence: undefined }), cell('android', 'not-run')] }] }] }] }] };
}
let container: HTMLDivElement;
let root: Root;
beforeEach(() => { container = document.createElement('div'); document.body.appendChild(container); root = createRoot(container); });
afterEach(() => { act(() => root.unmount()); container.remove(); vi.restoreAllMocks(); });
async function render(data = fixture(), selection: SuiteSelection = {}) {
  const onSelect = vi.fn(); const onOpenEvidence = vi.fn(); const client = { suites: vi.fn().mockResolvedValue(data) };
  await act(async () => root.render(<RetraceSuites client={client} selection={selection} onSelect={onSelect} onOpenEvidence={onOpenEvidence} />));
  return { onSelect, onOpenEvidence, client };
}
function button(label: string) { return [...container.querySelectorAll('button')].find(b => b.textContent === label)!; }

describe('RetraceSuites', () => {
  it('counts expected cells including missing Android, separates planes and labels imported assertions', async () => {
    await render();
    expect(container.querySelector('.suites__build-heading .suites__counts')?.textContent).toBe('0 / 3 passed1 failed1 incomplete1 not run');
    const matrix = container.querySelector('.suites__matrix')!;
    expect(matrix.textContent).toContain('WebiOSAndroid');
    expect(matrix.querySelector('button[aria-label*="Android"]')?.textContent).toBe('0 / 1 passed1 not run');
    expect(container.querySelector('.suites__plane-coverage')?.textContent).toBe('functional2 / 3required cells passedwire1 / 3required cells passedvisual0 / 3required cells passed');
    expect(container.textContent).toContain('External runner reports');
    expect(container.textContent).toContain('Dirty source snapshot');
    expect(container.textContent).toContain('legacy-1');
    expect(container.textContent).toContain('strict-1');
    expect(container.textContent).not.toContain('100%');
    expect(container.querySelector('.suites__flow')).toBeNull();
    expect(container.textContent).toContain('Select a feature or platform above');
    expect(container.querySelector('.suites__provenance-details')?.hasAttribute('open')).toBe(false);
  });
  it('selects a matrix cell with full suite/build identity and filters flow details', async () => {
    const { onSelect } = await render(fixture(), { suiteId: undefined, buildId: undefined, featureId: undefined, platform: undefined });
    await act(async () => (container.querySelector('button[aria-label*="Account access, Android"]') as HTMLButtonElement).click());
    expect(onSelect).toHaveBeenCalledWith({ suiteId: 'migration', buildId: 'build-1', featureId: 'auth', platform: 'android' });
    await render(fixture(), { suiteId: 'migration', buildId: 'build-1', featureId: 'auth', platform: 'android' });
    const flow = container.querySelector('.suites__flow')!;
    expect(flow.textContent).toContain('AndroidNot run');
    expect(flow.textContent).toContain('No runner result');
    expect(flow.querySelector('button')).toBeNull();
    expect(flow.querySelector('.suites__status--pass')).toBeNull();
  });
  it('shows required planes, latest failure, earlier passing history and exact pair evidence', async () => {
    const { onOpenEvidence } = await render(fixture(), { featureId: 'auth', platform: 'web' });
    const flow = container.querySelector('.suites__flow')!;
    await act(async () => flow.querySelector('summary')!.click());
    expect(flow.querySelector('details')?.open).toBe(true);
    expect(flow.querySelector('.suites__flow-cell > .suites__section-heading')?.textContent).toBe('WebFailed');
    expect(flow.textContent).toContain('Required: functional, wire, visual');
    expect(flow.textContent).toContain('Response contract changed');
    expect(flow.querySelector('details')?.textContent).toContain('old-pass');
    expect(flow.querySelector('details summary')?.textContent).toBe('Attempt history (2)');
    await act(async () => button('Open pair comparison').click());
    expect(onOpenEvidence).toHaveBeenCalledWith(latest.evidence);
  });
  it('opens run evidence without manufacturing a pair and states missing links', async () => {
    const data = fixture();
    data.suites[0].builds[0].features[0].flows[0].platforms[0].latest = { ...latest, evidence: { app: 'taxi', flow: 'login', runId: 'run-2' } };
    const { onOpenEvidence } = await render(data, { featureId: 'auth' });
    expect(container.textContent).toContain('No linked comparison evidence');
    await act(async () => button('Open run evidence').click());
    expect(onOpenEvidence).toHaveBeenCalledWith({ app: 'taxi', flow: 'login', runId: 'run-2' });
  });
  it('shows onboarding with no suites, retains configured suites with no builds, rejects stale selections', async () => {
    await render({ suites: [] });
    expect(container.textContent).toContain('No suites configured yet');
    expect(container.textContent).toContain('retrace.suites.json');
    const data = fixture(); data.suites[0].builds = [];
    await render(data);
    expect(container.textContent).toContain('No reports imported yet');
    expect(container.querySelector('select')?.textContent).toContain('Legacy to Taxi');
    await render(fixture(), { buildId: 'gone' });
    expect(container.textContent).toContain('Build unavailable');
    expect(container.querySelector('.suites__matrix')).toBeNull();
  });
  it('does not count optional planes as required coverage or empty cells as passes', async () => {
    const data = fixture(); const flow = data.suites[0].builds[0].features[0].flows[0];
    flow.platforms.forEach(c => { c.requiredPlanes = ['functional']; });
    await render(data);
    expect(container.querySelector('.suites__plane-coverage')?.textContent).toContain('visual0 / 0required cells passed');
    expect(container.textContent).not.toContain('100%');
  });
  it('reports malformed inventory errors without retaining green content and retries', async () => {
    const client = { suites: vi.fn().mockRejectedValueOnce(new Error('Invalid report: required visual plane missing')).mockResolvedValueOnce(fixture()) };
    await act(async () => root.render(<RetraceSuites client={client} selection={{}} onSelect={vi.fn()} onOpenEvidence={vi.fn()} />));
    expect(container.querySelector('[role=alert]')?.textContent).toContain('required visual plane missing');
    expect(container.querySelector('.suites__matrix')).toBeNull();
    await act(async () => button('Try again').click());
    expect(container.querySelector('.suites__matrix')).not.toBeNull();
    expect(client.suites).toHaveBeenCalledTimes(2);
  });
});


it('clears old build coverage while a different client is loading and ignores late responses', async () => {
  await render();
  let resolve!: (value: SuitesResponse) => void;
  const pending = { suites: () => new Promise<SuitesResponse>(done => { resolve = done; }) };
  await act(async () => root.render(<RetraceSuites client={pending} selection={{}} onSelect={vi.fn()} onOpenEvidence={vi.fn()} />));
  expect(container.querySelector('.suites__matrix')).toBeNull();
  expect(container.textContent).toContain('Loading comparison suites');
  await render({ suites: [] });
  await act(async () => resolve(fixture()));
  expect(container.textContent).toContain('No suites configured yet');
  expect(container.querySelector('.suites__matrix')).toBeNull();
});
