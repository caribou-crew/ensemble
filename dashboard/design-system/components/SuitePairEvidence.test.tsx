import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { beforeEach, afterEach, expect, it, vi } from 'vitest';
import type { Summary } from '../retraceTypes';
import SuitePairEvidence from './SuitePairEvidence';
function pairSummary(): Summary {
  return {
    schema: 'retrace-diff/1',
    app: 'web',
    flow: 'wallet-home',
    verdict: 'changed',
    a: { runId: 'reference', kind: 'bundle', dir: '/a', manifest: { app: 'web' } as never },
    b: { runId: '20260904T120000Z-abc1234', kind: 'run', dir: '/b', manifest: { app: 'mobile' } as never },
    quarantined: [],
    checkpoints: [
      {
        name: 'home',
        verdict: 'changed',
        diffPct: 1.5,
        diffPctFine: 1.5,
        numDiff: 10,
        images: { a: 'a.png', b: 'b.png', diff: 'diff.png', overlay: 'overlay.png' },
        at: '2026-09-04T12:00:00Z',
      },
    ],
    wire: { paired: [], missing: [], extra: [] },
    sections: [],
    hops: { newRoutes: [], goneRoutes: [], serviceCounts: [], routeFailures: [] } as never,
    unexpectedStatuses: [],
    perf: { status: 'unset', measuredMs: 0, budgetMs: 0 },
    conformance: [],
    openApiConfigured: false,
    capture: { a: { status: 'ok', summary: 'ok' }, b: { status: 'ok', summary: 'ok' } },
    counts: {
      checkpoints: 0,
      pixelChanged: 0,
      wirePaired: 0,
      wireChanged: 0,
      wireMoved: 0,
      wireMissing: 0,
      wireExtra: 0,
      violations: 0,
      hopNew: 0,
      hopGone: 0,
      unexpectedStatuses: 0,
      conformance: 0,
    },
    gates: [],
    budgets: [],
    unmeasuredGates: [],
    suppressions: [],
    triage: { label: '', rule: '', signals: { pixel: false, wire: false, hop: false, spec: false, capture: false } },
  };
}


let container: HTMLDivElement;
let root: Root;
beforeEach(() => { container = document.createElement('div'); document.body.appendChild(container); root = createRoot(container); });
afterEach(() => { act(() => root.unmount()); container.remove(); });
const evidence = { app: 'candidate', flow: 'login', runId: 'pinned-run', pairId: 'pinned-pair' };
const clientFor = (summary = pairSummary()) => ({ pair: vi.fn().mockResolvedValue({ summary }), pairShotUrl: vi.fn((_a, _f, run, pair, side, name) => `/pairs/${run}/${pair}/${side}/${name}`) });
function button(label: string) { return [...container.querySelectorAll('button')].find(b => b.textContent === label)!; }
it('loads exact historical pair, starts with originals and switches evidence in place', async () => {
  const client = clientFor(); const open = vi.fn();
  await act(async () => root.render(<SuitePairEvidence client={client} evidence={evidence} onOpenFull={open} />));
  expect(client.pair).toHaveBeenCalledWith('candidate', 'login', 'pinned-run', 'pinned-pair');
  expect([...container.querySelectorAll('img')].map(i => i.getAttribute('src'))).toEqual(['/pairs/pinned-run/pinned-pair/a/home', '/pairs/pinned-run/pinned-pair/b/home']);
  await act(async () => button('Diff').click());
  expect(container.querySelector('img')?.getAttribute('src')).toBe('/pairs/pinned-run/pinned-pair/diff/home');
  await act(async () => button('Wire traffic').click());
  expect(container.querySelector('img')).toBeNull();
  expect(open).not.toHaveBeenCalled();
  await act(async () => button('Full comparison ↗').click());
  expect(open).toHaveBeenCalledTimes(1);
});
it('keeps failed capture warning visible, and never presents quarantined captures as comparisons', async () => {
  const summary = pairSummary(); summary.capture.b = { status: 'failed', summary: 'Incomplete recording' };
  await act(async () => root.render(<SuitePairEvidence client={clientFor(summary)} evidence={evidence} onOpenFull={vi.fn()} />));
  expect(container.textContent).toContain('Incomplete recording');
  summary.verdict = 'quarantined';
  await act(async () => root.render(<SuitePairEvidence client={clientFor(summary)} evidence={evidence} onOpenFull={vi.fn()} />));
  expect(container.textContent).toContain('not compared');
  expect(container.querySelector('img')).toBeNull();
});
it('clears images while loading a new pair and ignores a late response', async () => {
  await act(async () => root.render(<SuitePairEvidence client={clientFor()} evidence={evidence} onOpenFull={vi.fn()} />));
  let resolve!: (v: {summary: Summary}) => void;
  const pending = { ...clientFor(), pair: vi.fn(() => new Promise<{ summary: Summary }>(done => { resolve = done; })) };
  await act(async () => root.render(<SuitePairEvidence client={pending} evidence={{...evidence, pairId: 'later'}} onOpenFull={vi.fn()} />));
  expect(container.querySelector('img')).toBeNull();
  expect(container.textContent).toContain('Loading recorded comparison');
  const empty = pairSummary(); empty.checkpoints = [];
  await act(async () => root.render(<SuitePairEvidence client={clientFor(empty)} evidence={{...evidence, pairId: 'empty'}} onOpenFull={vi.fn()} />));
  await act(async () => resolve({summary: pairSummary()}));
  expect(container.querySelector('img')).toBeNull();
  expect(container.textContent).toContain('No screenshots were recorded');
});
it('shows an explicit error instead of stale evidence when the pair cannot be loaded', async () => {
  const client = clientFor(); client.pair.mockRejectedValue(new Error('Pair unavailable'));
  await act(async () => root.render(<SuitePairEvidence client={client} evidence={evidence} onOpenFull={vi.fn()} />));
  expect(container.querySelector('[role=alert]')?.textContent).toContain('Pair unavailable');
  expect(container.querySelector('img')).toBeNull();
});
