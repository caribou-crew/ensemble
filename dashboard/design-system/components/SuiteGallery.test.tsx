import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { SuiteGallery as Gallery, SuiteGalleryTile } from '../suiteTypes';
import SuiteGallery from './SuiteGallery';

const planes = { functional: 'pass', wire: 'failed', visual: 'incomplete' } as const;
const sha = 'a'.repeat(64);
const notRun: SuiteGalleryTile = { platform: 'android', status: 'not-run', planes: { functional: 'not-run', wire: 'not-run', visual: 'not-run' }, screens: [], wire: { represented: false, plane: 'not-run', note: 'No result has been imported for this lane, so there is no wire evidence.' } };
function fixture(): Gallery {
  return {
    suiteId: 'taxi', title: 'Taxi', buildId: 'b1', git: { sha: '0'.repeat(40), branch: 'main', dirty: false }, baselineId: 'legacy', policyId: 'p1', updatedAt: '2026-09-20T00:00:00Z', platforms: ['web', 'ios', 'android'],
    rows: [
      { feature: { id: 'wallet', title: 'Wallet' }, flow: { id: 'home', title: 'Wallet home' }, tiles: [
        { platform: 'web', status: 'failed', planes, reason: 'Balance differs', attemptId: 'web-1', evidence: { app: 'cand', flow: 'home', runId: 'r1', pairId: 'p1' },
          screens: [], pair: { app: 'cand', flow: 'home', runId: 'r1', pairId: 'p1', available: true, checkpoint: 'final', verdict: 'changed', checkpoints: [{ name: 'final', verdict: 'changed', diffPct: 4.56 }] },
          wire: { represented: true, plane: 'failed', counts: { paired: 9, changed: 2, moved: 0, missing: 1, extra: 3, violations: 0 }, note: '' } },
        { platform: 'ios', status: 'pass', planes: { functional: 'pass', wire: 'not-applicable', visual: 'not-applicable' }, attemptId: 'ios-1', screens: [{ label: 'Final screen', sha256: sha, media: 'image/png' }],
          wire: { represented: false, plane: 'not-applicable', note: 'No reference wire exists for iOS.' } },
        notRun,
      ] },
      { feature: { id: 'wallet', title: 'Wallet' }, flow: { id: 'atm', title: 'Find ATM' }, tiles: [
        { platform: 'web', status: 'pass', planes: { functional: 'pass', wire: 'pass', visual: 'pass' }, attemptId: 'web-1', screens: [], pair: { app: 'cand', flow: 'atm', runId: 'r2', pairId: 'p2', available: false, error: 'linked comparison is not readable' },
          wire: { represented: false, plane: 'pass', note: 'Wire diff not represented here: the linked comparison could not be read.' } },
        { ...notRun, platform: 'ios' }, notRun,
      ] },
    ],
  };
}
let container: HTMLDivElement; let root: Root;
beforeEach(() => { container = document.createElement('div'); document.body.appendChild(container); root = createRoot(container); });
afterEach(() => { act(() => root.unmount()); container.remove(); });
function clientFor(data: Gallery | Error) {
  return {
    suiteGallery: vi.fn(async () => { if (data instanceof Error) throw data; return data; }),
    suiteScreenUrl: (s: string, a: string, h: string) => `/screen/${s}/${a}/${h}`,
    pairShotUrl: (app: string, flow: string, run: string, pair: string, side: string, name: string) => `/pair/${app}/${flow}/${run}/${pair}/${side}/${name}`,
  };
}
async function render(data: Gallery | Error = fixture()) {
  const onOpenEvidence = vi.fn(); const client = clientFor(data);
  await act(async () => root.render(<SuiteGallery client={client} suiteId="taxi" buildId="b1" onOpenEvidence={onOpenEvidence} />));
  return { client, onOpenEvidence };
}
const srcs = () => [...container.querySelectorAll('img')].map(i => i.getAttribute('src'));
async function choose(label: string, value: string) {
  const el = container.querySelector(`[aria-label="${label}"]`) as HTMLSelectElement;
  await act(async () => { el.value = value; el.dispatchEvent(new Event('change', { bubbles: true })); });
}

describe('SuiteGallery', () => {
  it('shows a saved pair as reference/candidate/diff and native lanes as their final screen', async () => {
    await render();
    expect(srcs()).toEqual(['/pair/cand/home/r1/p1/a/final', '/pair/cand/home/r1/p1/b/final', '/pair/cand/home/r1/p1/diff/final', `/screen/taxi/ios-1/${sha}`]);
    expect(container.querySelector('img[alt="Web diff — Wallet home"]')).not.toBeNull();
    expect(container.textContent).toContain('changed 4.6%');
  });
  it('calls out wire counts when a comparison exists and states plainly when it is not represented', async () => {
    await render();
    const web = container.querySelector('[data-platform="web"] .gallery__wire')!;
    expect(web.textContent).toContain('9 paired · 2 changed · 1 missing · 3 extra');
    expect(web.textContent).toContain('runner: wire failed');
    const ios = container.querySelector('[data-platform="ios"] .gallery__wire')!;
    expect(ios.textContent).toContain('Wire diff not represented');
    expect(ios.textContent).toContain('No reference wire exists for iOS.');
    expect(ios.textContent).toContain('an assertion; no diff is shown');
    expect(ios.textContent).not.toContain('paired');
  });
  it('trusts represented over stray counts, so unrepresented wire can never look compared', async () => {
    const g = fixture();
    g.rows[0].tiles[1].wire = { represented: false, plane: 'pass', note: 'stale link', counts: { paired: 5, changed: 0, moved: 0, missing: 0, extra: 0, violations: 0 } };
    await render(g);
    const ios = container.querySelector('[data-platform="ios"] .gallery__wire')!;
    expect(ios.textContent).toContain('Wire diff not represented');
    expect(ios.textContent).not.toContain('5 paired');
  });
  it('never presents a missing lane or an unreadable pair as compared or passing', async () => {
    await render();
    const lane = container.querySelector('[data-platform="android"]')!;
    expect(lane.textContent).toContain('No result imported for this lane: no screenshots and no wire evidence.');
    expect(lane.querySelector('.suites__status')?.textContent).toBe('Not run');
    expect(lane.querySelector('.gallery__wire')).toBeNull();
    expect(lane.querySelector('img')).toBeNull();
    expect(container.querySelector('[role=alert]')?.textContent).toContain('Linked comparison unavailable: linked comparison is not readable');
    const atmWeb = container.querySelectorAll('.gallery__row')[1].querySelector('[data-platform="web"] .gallery__wire')!;
    expect(atmWeb.textContent).toContain('Wire diff not represented');
  });
  it('filters to wire-not-represented, wire-changed and searched flows', async () => {
    await render();
    const names = () => [...container.querySelectorAll('.gallery__flow')].map(h => h.textContent);
    expect(names()).toEqual(['Wallet home', 'Find ATM']);
    await choose('Gallery filter', 'wire-changed');
    expect(names()).toEqual(['Wallet home']);
    await choose('Gallery filter', 'wire-absent');
    expect(names()).toEqual(['Wallet home', 'Find ATM']);
    await choose('Gallery filter', 'all');
    const input = container.querySelector('input[aria-label="Search gallery flows"]') as HTMLInputElement;
    await act(async () => { const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!; set.call(input, 'atm'); input.dispatchEvent(new Event('input', { bubbles: true })); });
    expect(names()).toEqual(['Find ATM']);
  });
  it('opens exact evidence and states the review caveats', async () => {
    const { onOpenEvidence } = await render();
    await act(async () => { (container.querySelector('[data-platform="web"] button') as HTMLButtonElement).click(); });
    expect(onOpenEvidence).toHaveBeenCalledWith({ app: 'cand', flow: 'home', runId: 'r1', pairId: 'p1' });
    expect(container.querySelector('[role=note]')?.textContent).toContain('nothing here is visually accepted');
  });
  it('reports a load failure instead of an empty board', async () => {
    await render(new Error('boom'));
    expect(container.querySelector('[role=alert]')?.textContent).toContain('boom');
    expect(container.querySelectorAll('.gallery__row')).toHaveLength(0);
  });
});
