import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { SuiteGallery as Gallery, SuiteGalleryTile, SuiteGallerySource } from '../suiteTypes';
import SuiteGallery from './SuiteGallery';

const sha = 'a'.repeat(64);
const src = (s: string, dirty = false, at = '2026-09-19T00:00:00Z'): SuiteGallerySource => ({ buildId: `b-${s}`, sha: s.repeat(40).slice(0, 40), branch: 'main', dirty, baselineId: 'legacy', policyId: 'p1', finishedAt: at });
const webSource = src('1', true), nativeSource = src('b', false, '2026-09-21T00:00:00Z');
const noRun: SuiteGalleryTile = { platform: 'android', status: 'not-run', planes: { functional: 'not-run', wire: 'not-run', visual: 'not-run' }, screens: [], wire: { represented: false, plane: 'not-run', note: 'No result has been imported for this lane, so there is no wire evidence.' } };
function fixture(): Gallery {
  return {
    suiteId: 'taxi', title: 'Taxi', scope: 'latest', buildId: '', git: { sha: '', branch: '', dirty: false }, baselineId: '', policyId: '', updatedAt: '', platforms: ['web', 'ios', 'android'],
    lanes: [{ platform: 'web', sources: [webSource] }, { platform: 'ios', sources: [nativeSource, src('c')] }, { platform: 'android', sources: [nativeSource] }],
    rows: [
      { feature: { id: 'wallet', title: 'Wallet' }, flow: { id: 'home', title: 'Wallet home' }, tiles: [
        { platform: 'web', status: 'failed', planes: { functional: 'pass', wire: 'pass', visual: 'incomplete' }, reason: 'Balance differs', attemptId: 'web-1', source: webSource, evidence: { app: 'cand', flow: 'home', runId: 'r1', pairId: 'p1' }, screens: [],
          pair: { app: 'cand', flow: 'home', runId: 'r1', pairId: 'p1', available: true, checkpoint: 'final', verdict: 'changed', checkpoints: [{ name: 'final', verdict: 'changed', diffPct: 4.56 }] },
          wire: { represented: true, plane: 'pass', counts: { paired: 9, changed: 2, moved: 1, missing: 1, extra: 3, violations: 0 }, note: '' } },
        { platform: 'ios', status: 'pass', planes: { functional: 'pass', wire: 'not-applicable', visual: 'not-applicable' }, attemptId: 'ios-1', source: nativeSource, screens: [{ label: 'Final screen', sha256: sha, media: 'image/png' }],
          wire: { represented: false, plane: 'not-applicable', note: 'No reference wire exists for iOS.' } },
        noRun,
      ] },
      { feature: { id: 'wallet', title: 'Wallet' }, flow: { id: 'atm', title: 'Find ATM' }, tiles: [
        { platform: 'web', status: 'pass', planes: { functional: 'pass', wire: 'pass', visual: 'pass' }, attemptId: 'web-1', source: webSource, evidence: { app: 'cand', flow: 'atm', runId: 'r2', pairId: 'p2' }, screens: [],
          pair: { app: 'cand', flow: 'atm', runId: 'r2', pairId: 'p2', available: false, error: 'linked comparison is not readable' },
          wire: { represented: false, plane: 'pass', note: 'Wire diff not represented here: the linked comparison could not be read.' } },
        { ...noRun, platform: 'ios' }, noRun,
      ] },
      { feature: { id: 'pay', title: 'Payments' }, flow: { id: 'send', title: 'Send money' }, tiles: [
        { platform: 'web', status: 'pass', planes: { functional: 'pass', wire: 'pass', visual: 'pass' }, attemptId: 'web-1', source: webSource, evidence: { app: 'cand', flow: 'send', runId: 'r3', pairId: 'p3' }, screens: [],
          pair: { app: 'cand', flow: 'send', runId: 'r3', pairId: 'p3', available: true, checkpoint: 'final', verdict: 'ok', checkpoints: [{ name: 'final', verdict: 'ok', diffPct: 0 }] },
          wire: { represented: true, plane: 'pass', counts: { paired: 5, changed: 0, moved: 0, missing: 0, extra: 0, violations: 0 }, note: '' } },
        { ...noRun, platform: 'ios' }, noRun,
      ] },
    ],
  };
}
let container: HTMLDivElement; let root: Root;
beforeEach(() => { container = document.createElement('div'); document.body.appendChild(container); root = createRoot(container); });
afterEach(() => { act(() => root.unmount()); container.remove(); });
function clientFor(data: Gallery | Error) {
  return {
    suiteGallery: vi.fn(async (_s: string, _b?: string) => { if (data instanceof Error) throw data; return data; }),
    suiteScreenUrl: (s: string, a: string, h: string) => `/screen/${s}/${a}/${h}`,
    pairShotUrl: (app: string, flow: string, run: string, pair: string, side: string, name: string) => `/pair/${app}/${flow}/${run}/${pair}/${side}/${name}`,
  };
}
async function render(data: Gallery | Error = fixture(), buildId?: string) {
  const onOpenEvidence = vi.fn(); const client = clientFor(data);
  await act(async () => root.render(<SuiteGallery client={client} suiteId="taxi" buildId={buildId} onOpenEvidence={onOpenEvidence} />));
  return { client, onOpenEvidence };
}
const click = async (el: Element | null) => { await act(async () => (el as HTMLElement).click()); };
const button = (label: string) => [...container.querySelectorAll('button')].find(b => b.textContent?.startsWith(label)) ?? null;
async function choose(label: string, value: string) {
  const el = container.querySelector(`[aria-label="${label}"]`) as HTMLSelectElement;
  await act(async () => { el.value = value; el.dispatchEvent(new Event('change', { bubbles: true })); });
}
const flows = () => [...container.querySelectorAll('.gallery__flow')].map(h => h.querySelector('span')?.textContent ?? h.textContent);

describe('SuiteGallery', () => {
  it('lays out reference, candidate and diff for web and the final screen for native platforms in one table', async () => {
    const { client } = await render();
    expect(client.suiteGallery).toHaveBeenCalledWith('taxi', undefined);
    expect([...container.querySelectorAll('thead th')].map(h => h.textContent)).toEqual(['Flow', 'Web reference', 'Web candidate', 'Web diff', 'iOS', 'Android', 'Network']);
    const srcs = [...container.querySelectorAll('img')].map(i => i.getAttribute('src'));
    expect(srcs).toEqual([
      '/pair/cand/home/r1/p1/a/final', '/pair/cand/home/r1/p1/b/final', '/pair/cand/home/r1/p1/diff/final', `/screen/taxi/ios-1/${sha}`,
      '/pair/cand/send/r3/p3/a/final', '/pair/cand/send/r3/p3/b/final', '/pair/cand/send/r3/p3/diff/final',
    ]);
    expect(container.textContent).toContain('changed 4.6%');
  });
  it('names the revision behind each platform column, including additional revisions', async () => {
    await render();
    const lanes = container.querySelector('.gallery__lanes')!.textContent!;
    expect(lanes).toContain('Web 1111111 dirty');
    expect(lanes).toContain('iOS bbbbbbb');
    expect(lanes).toContain('+1 other revision');
    expect(lanes).toContain('2026-09-21');
    expect(container.querySelector('[role=note]')?.textContent).toContain('nothing here is visually accepted');
  });
  it('keeps the screens view quiet: missing lanes are a dash, and there are no per-tile wire warning boxes', async () => {
    await render();
    expect(container.textContent).not.toContain('Wire diff not represented');
    expect(container.querySelector('.gallery__dot--not-run')).not.toBeNull();
    const atm = [...container.querySelectorAll('tbody tr')].find(r => r.textContent?.includes('Find ATM'))!;
    expect(atm.querySelector('[role=alert]')?.textContent).toContain('comparison unavailable');
    expect(atm.querySelectorAll('.gallery__empty').length).toBeGreaterThanOrEqual(3);
    expect(atm.querySelector('img')).toBeNull();
  });
  it('shows a compact network chip only for a represented comparison and opens exactly that comparison', async () => {
    const { onOpenEvidence } = await render();
    const chips = [...container.querySelectorAll('.gallery__chip')];
    expect(chips.map(c => c.textContent)).toEqual(['2 changed · 1 missing · 3 extra · 1 moved', 'no diff']);
    expect(chips[0].className).toContain('gallery__chip--diff');
    expect(chips[1].className).toContain('gallery__chip--clean');
    await click(chips[0]);
    expect(onOpenEvidence).toHaveBeenCalledWith({ app: 'cand', flow: 'home', runId: 'r1', pairId: 'p1' });
  });
  it('trusts represented over stray counts so an unrepresented lane never looks compared', async () => {
    const g = fixture();
    g.rows[0].tiles[1].wire = { represented: false, plane: 'pass', note: 'stale', counts: { paired: 5, changed: 4, moved: 0, missing: 0, extra: 0, violations: 0 } };
    g.rows[0].tiles[0].wire = { represented: false, plane: 'pass', note: 'x' };
    await render(g);
    expect([...container.querySelectorAll('.gallery__chip')].map(c => c.textContent)).toEqual(['no diff']);
  });
  it('lists network differences in their own section, most serious first, with what the runner asserted', async () => {
    const g = fixture();
    // Input order is the opposite of severity: Wallet home only has many changed exchanges,
    // Send money has a missing request.
    g.rows[0].tiles[0].wire.counts = { paired: 20, changed: 19, moved: 0, missing: 0, extra: 0, violations: 0 };
    g.rows[2].tiles[0].wire.counts = { paired: 5, changed: 0, moved: 0, missing: 2, extra: 0, violations: 0 };
    await render(g);
    await click(button('Network'));
    const rows = [...container.querySelectorAll('.gallery__table--net tbody tr')].map(r => r.textContent);
    expect(rows).toHaveLength(2);
    expect(rows[0]).toContain('Send money');
    expect(rows[1]).toContain('Wallet home');
    expect(rows[1]).toContain('passed');
    expect(container.querySelector('.gallery__network > p')?.textContent).toContain('1 with missing or extra requests or rule violations (listed first), 1 with only changed or moved');
    expect(container.querySelector('[role=alert]')?.textContent).toContain('Find ATM (Web)');
    expect(container.textContent).toContain('No network comparison exists for: iOS, Android');
    expect(container.querySelector('.gallery__nowire')!.textContent).toContain('Wallet home · iOS — No reference wire exists for iOS.');
  });
  it('filters to visual or network differences and by search, and reports counts', async () => {
    await render();
    expect(flows()).toEqual(['Wallet home', 'Find ATM', 'Send money']);
    await choose('Gallery filter', 'network');
    expect(flows()).toEqual(['Wallet home']);
    await choose('Gallery filter', 'visual');
    expect(flows()).toEqual(['Wallet home']);
    await choose('Gallery filter', 'attention');
    expect(flows()).toEqual(['Wallet home', 'Find ATM', 'Send money']);
    await choose('Gallery filter', 'all');
    const input = container.querySelector('input[aria-label="Search gallery flows"]') as HTMLInputElement;
    await act(async () => { Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input, 'send'); input.dispatchEvent(new Event('input', { bubbles: true })); });
    expect(flows()).toEqual(['Send money']);
    expect(container.textContent).toContain('1 of 3 flows');
    expect(button('Network (')?.textContent).toBe('Network (1)');
    const g = fixture();
    g.rows[0].tiles[0].wire.counts = { paired: 9, changed: 9, moved: 0, missing: 0, extra: 0, violations: 0 };
    act(() => root.unmount()); root = createRoot(container);
    await render(g);
    expect(button('Network')?.textContent).toBe('Network');
  });
  it('offers a single-revision scope only when a build is selected, and requests exactly that build', async () => {
    const { client } = await render(fixture(), 'build-9');
    expect(client.suiteGallery).toHaveBeenLastCalledWith('taxi', undefined);
    await choose('Gallery scope', 'build');
    expect(client.suiteGallery).toHaveBeenLastCalledWith('taxi', 'build-9');
    act(() => root.unmount()); root = createRoot(container);
    await render();
    expect(container.querySelector('[aria-label="Gallery scope"]')).toBeNull();
  });
  it('reports a load failure instead of an empty board', async () => {
    await render(new Error('boom'));
    expect(container.querySelector('[role=alert]')?.textContent).toContain('boom');
    expect(container.querySelector('table')).toBeNull();
  });
});
