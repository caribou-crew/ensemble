import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { SuitesResponse } from '@ensemble/design-system/suiteTypes';
import RetraceView from './RetraceView';

let container: HTMLDivElement;
let root: Root;
beforeEach(() => { window.history.replaceState({}, '', '/?view=retrace'); container = document.createElement('div'); document.body.appendChild(container); root = createRoot(container); });
afterEach(() => { act(() => root.unmount()); container.remove(); vi.unstubAllGlobals(); });
function fixture(pair: boolean): SuitesResponse {
  const counts = { total: 1, passed: 0, failed: 1, incomplete: 0, notRun: 0 };
  return { suites: [{ id: 'migration', title: 'Legacy to Taxi', version: 'v1', platforms: ['ios'], builds: [{ id: 'candidate', git: { sha: 'a'.repeat(40), branch: 'taxi', dirty: false }, baselineId: 'legacy', policyId: 'strict', updatedAt: '2026-09-20T00:00:00Z', counts, platforms: [{ platform: 'ios', counts }], features: [{ id: 'login', title: 'Account access', counts, platforms: [{ platform: 'ios', counts }], flows: [{ id: 'login', title: 'Sign in', platforms: [{ platform: 'ios', status: 'failed', requiredPlanes: ['functional'], history: [], latest: { attemptId: 'attempt', startedAt: '2026-09-20T00:00:00Z', finishedAt: '2026-09-20T00:00:00Z', planes: { functional: 'failed', wire: 'not-applicable', visual: 'not-applicable' }, evidence: { app: 'taxi ios', flow: 'sign in', runId: 'run-1', ...(pair ? { pairId: 'pair-1' } : {}) } } }] }] }] }] }] };
}
function stub(pair = false) {
  const calls: string[] = [];
  vi.stubGlobal('fetch', vi.fn(async (input: string) => {
    calls.push(input);
    const suite = input === '/api/retrace/suites';
    return { ok: suite, status: suite ? 200 : 404, statusText: 'Not found', text: async () => JSON.stringify(suite ? fixture(pair) : { error: 'Linked comparison not available' }) };
  }));
  return calls;
}
async function mount() { await act(async () => root.render(<RetraceView />)); }

describe('embedded commit suites', () => {
  it('uses the configured embedded root directly and persists selection across remount', async () => {
    const calls = stub(); await mount();
    expect(calls).toEqual(['/api/retrace/suites']);
    await act(async () => (container.querySelector('.suites__matrix td button') as HTMLButtonElement).click());
    expect(Object.fromEntries(new URLSearchParams(window.location.search))).toEqual({ view: 'retrace', suite: 'migration', suiteBuild: 'candidate', suiteFeature: 'login', suitePlatform: 'ios' });
    act(() => root.unmount()); root = createRoot(container); await mount();
    expect(container.querySelector('[aria-label="Flow details"] h2')?.textContent).toBe('Account access · iOS');
  });
  it.each([false, true])('opens scoped evidence and returns to the same suite (pair=%s)', async pair => {
    const calls = stub(pair); await mount();
    await act(async () => (container.querySelector('.suites__matrix td button') as HTMLButtonElement).click());
    await act(async () => [...container.querySelectorAll('button')].find(b => b.textContent === (pair ? 'Open pair comparison' : 'Open run evidence'))!.click());
    expect(calls).toContain(pair ? '/api/retrace/pairs/taxi%20ios/sign%20in/run-1/pair-1' : '/api/retrace/queue/taxi%20ios/sign%20in/runs/run-1?exact=1');
    expect(container.textContent).toContain('Linked comparison not available');
    expect(new URLSearchParams(window.location.search).get('retraceRun')).toBe('run-1');
    await act(async () => [...container.querySelectorAll('button')].find(b => b.textContent === '← Comparison suites')!.click());
    expect(container.querySelector('[aria-label="Flow details"] h2')?.textContent).toBe('Account access · iOS');
    expect(new URLSearchParams(window.location.search).has('retraceRun')).toBe(false);
  });
});

it.each([false, true])('pins embedded suite screenshots and labels return navigation (pair=%s)', async pair => {
  const calls: string[] = [];
  vi.stubGlobal('fetch', vi.fn(async (input: string) => {
    calls.push(input);
    const body = input === '/api/retrace/suites' ? fixture(pair) : { summary: {
      verdict: 'quarantined', a: { runId: 'reference', manifest: { app: 'legacy' } },
      b: { runId: 'run-1', manifest: { mode: 'standalone', checkpoints: [{ name: 'login' }] } },
      triage: { label: '' }, gates: [], quarantined: [{ side: 'b', reason: 'capture incomplete' }],
    } };
    return { ok: true, status: 200, text: async () => JSON.stringify(body) };
  }));
  await mount();
  await act(async () => (container.querySelector('.suites__matrix td button') as HTMLButtonElement).click());
  await act(async () => [...container.querySelectorAll('button')].find(b => b.textContent === (pair ? 'Open pair comparison' : 'Open run evidence'))!.click());
  expect(container.querySelector('.item__back')?.textContent).toBe('← back to comparison suites');
  const provenance = container.querySelector('[aria-label="Run comparison provenance"]');
  if (pair) expect(provenance).toBeNull();
  else expect(provenance?.textContent).toBe('This run is pinned. Its comparison uses the current reference and policy; it does not reproduce the imported suite verdict. For historical baseline evidence, link a saved pair made from retained, concrete run IDs.');
  if (pair) return;
  expect(calls).toContain('/api/retrace/queue/taxi%20ios/sign%20in/runs/run-1?exact=1');
  expect(container.querySelector('.item__capture-img')?.getAttribute('src')).toBe('/api/retrace/shots/taxi%20ios/sign%20in/runs/run-1/b/login?exact=1');
});
