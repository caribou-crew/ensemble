import { readFileSync } from 'node:fs';
import type { Page } from '@playwright/test';
import type { SuiteAttemptResult, SuiteFlowCell, SuiteGallery, SuiteGalleryTile, SuitesResponse } from '../../design-system/suiteTypes';
import type { Summary } from '../../design-system/retraceTypes';

const counts = (passed = 0, failed = 0, incomplete = 0, notRun = 0) => ({ total: passed + failed + incomplete + notRun, passed, failed, incomplete, notRun });
const stamp = '2026-01-01T12:00:00Z';
const trust = { status: 'ok' as const, summary: 'Synthetic complete capture' };
const attempt = (id: string, status: SuiteFlowCell['status']): SuiteAttemptResult => ({
  attemptId: id, startedAt: stamp, finishedAt: stamp,
  planes: { functional: status === 'failed' ? 'failed' : 'pass', wire: 'pass', visual: status === 'pass' ? 'pass' : 'incomplete' },
  evidence: { app: 'example-candidate', flow: id, runId: 'pinned-run', pairId: 'pinned-reference' },
});
const flow = (id: string, title: string, status: SuiteFlowCell['status'], evidence = true) => {
  const latest = status === 'not-run' ? undefined : attempt(id, status);
  if (latest && !evidence) delete latest.evidence;
  return { id, title, platforms: [{ platform: 'web' as const, status, requiredPlanes: ['functional', 'wire', 'visual'] as SuiteFlowCell['requiredPlanes'], latest, history: latest ? [latest] : [] }] };
};
export function suitesFixture(): SuitesResponse {
  return { suites: [{ id: 'example', title: 'Example migration', version: '1', platforms: ['web'], builds: [{
    id: 'build-one', git: { sha: 'a'.repeat(40), branch: 'example', dirty: false }, baselineId: 'baseline-one', policyId: 'strict', updatedAt: stamp,
    counts: counts(1, 1, 3, 1), platforms: [{ platform: 'web', counts: counts(1, 1, 3, 1) }],
    features: [
      { id: 'auth', title: 'Authentication', counts: counts(1, 0, 1), platforms: [{ platform: 'web', counts: counts(1, 0, 1) }], flows: [flow('sign-in', 'Sign in', 'incomplete'), flow('sign-out', 'Sign out', 'pass')] },
      { id: 'account', title: 'Account', counts: counts(0, 1, 2, 1), platforms: [{ platform: 'web', counts: counts(0, 1, 2, 1) }], flows: [flow('profile', 'Edit profile', 'failed'), flow('missing-link', 'Missing report evidence', 'incomplete', false), flow('missing-shots', 'No recorded screenshots', 'incomplete'), flow('not-run', 'Scheduled flow', 'not-run')] },
    ],
  }] }] };
}
const source = { buildId: 'build-one', sha: 'a'.repeat(40), branch: 'example', dirty: false, baselineId: 'baseline-one', policyId: 'strict', finishedAt: stamp };
const noResult: SuiteGalleryTile = { platform: 'ios', status: 'not-run', planes: { functional: 'not-run', wire: 'not-run', visual: 'not-run' }, screens: [], wire: { represented: false, plane: 'not-run', note: 'No result has been imported for this lane, so there is no wire evidence.' } };
const webTile = (flowId: string, wire: SuiteGalleryTile['wire']): SuiteGalleryTile => ({
  platform: 'web', status: 'incomplete', planes: { functional: 'pass', wire: 'pass', visual: 'incomplete' }, attemptId: flowId, source,
  evidence: { app: 'example-candidate', flow: flowId, runId: 'pinned-run', pairId: 'pinned-reference' }, screens: [],
  pair: { app: 'example-candidate', flow: flowId, runId: 'pinned-run', pairId: 'pinned-reference', available: true, checkpoint: flowId, verdict: 'changed', checkpoints: [{ name: flowId, verdict: 'changed', diffPct: 1 }] }, wire,
});
/** The gallery board: two web pairs (one with a missing request), one flow with no pair, iOS not run. */
export function galleryFixture(): SuiteGallery {
  const row = (feature: [string, string], flow: [string, string], web: SuiteGalleryTile) => ({ feature: { id: feature[0], title: feature[1] }, flow: { id: flow[0], title: flow[1] }, tiles: [web, { ...noResult }] });
  const bare: SuiteGalleryTile = { ...webTile('missing-link', { represented: false, plane: 'pass', note: 'Wire diff not represented here: no saved comparison is linked to this result.' }), pair: undefined, evidence: undefined, status: 'pass' };
  return {
    suiteId: 'example', title: 'Example migration', scope: 'latest', buildId: '', git: { sha: '', branch: '', dirty: false }, baselineId: '', policyId: '', updatedAt: stamp, platforms: ['web', 'ios'],
    lanes: [{ platform: 'web', sources: [source] }, { platform: 'ios', sources: [] }],
    rows: [
      row(['auth', 'Authentication'], ['sign-in', 'Sign in'], webTile('sign-in', { represented: true, plane: 'pass', counts: { paired: 4, changed: 1, moved: 0, missing: 1, extra: 0, violations: 0 }, note: '' })),
      row(['account', 'Account'], ['profile', 'Edit profile'], webTile('profile', { represented: true, plane: 'pass', counts: { paired: 3, changed: 2, moved: 1, missing: 0, extra: 0, violations: 0 }, note: '' })),
      row(['account', 'Account'], ['missing-link', 'Missing report evidence'], bare),
    ],
  };
}
export function pairFixture(flowId: string): Summary {
  const manifest: Summary['a']['manifest'] = { schema: 'retrace/1', app: 'example', flow: flowId, runId: 'pinned-run', mode: 'standalone', git: { sha: 'a'.repeat(40), branch: 'example', dirty: false }, startedAt: stamp, finishedAt: stamp, checkpoints: [], groups: [], capture: trust, wire: { calls: 1, recorded: true }, test: { command: 'synthetic fixture', exitCode: 0, durationMs: 1 }, env: { go: 'fixture', platform: 'fixture', retrace: 'fixture' } };
  return {
    schema: 'retrace-diff/1', app: 'example-candidate', flow: flowId, verdict: 'changed',
    a: { runId: 'reference-run', kind: 'run', dir: '/synthetic/a', manifest: { ...manifest, app: 'example-reference', runId: 'reference-run' } },
    b: { runId: 'pinned-run', kind: 'run', dir: '/synthetic/b', manifest: { ...manifest, app: 'example-candidate' } },
    checkpoints: flowId === 'missing-shots' ? [] : [{ name: flowId, verdict: 'changed', diffPct: 1, diffPctFine: 1, numDiff: 1, at: stamp, images: { a: 'a.svg', b: 'b.svg', diff: 'diff.svg', overlay: 'overlay.svg' } }],
    wire: { paired: [], missing: [], extra: [] },
    sections: [{ name: 'Profile API', counts: { changed: 1 }, entries: [{ method: 'GET', normalizedPath: '/example/profile', seqA: 1, seqB: 1, posA: 0, posB: 0, moved: false, truncated: false, classes: ['changed'], bodyDiff: [], bodyTolerated: [], bodyViolations: [], bodyIgnored: [], orderingChanges: [], headerDiff: [], headerIgnored: [] }] }],
    hops: { newRoutes: [], goneRoutes: [], serviceCounts: [], routeFailures: [], hopRequireConfigured: false } as Summary['hops'],
    unexpectedStatuses: [], perf: { status: 'unset', measuredMs: 0, budgetMs: 0 }, conformance: [], openApiConfigured: false,
    capture: { a: trust, b: trust }, counts: { checkpoints: flowId === 'missing-shots' ? 0 : 1, pixelChanged: flowId === 'missing-shots' ? 0 : 1, wirePaired: 1, wireChanged: 1, wireMoved: 0, wireMissing: 0, wireExtra: 0, violations: 0, hopNew: 0, hopGone: 0, unexpectedStatuses: 0, conformance: 0 },
    gates: [], budgets: [], unmeasuredGates: [], suppressions: [], quarantined: [], triage: { label: 'client-ui', rule: '', signals: { pixel: true, wire: false, hop: false, spec: false, capture: false } },
  };
}
export async function installFixtures(page: Page, options: { brokenImage?: boolean } = {}) {
  const unexpected: string[] = [];
  const images = ['reference', 'candidate'].map(name => readFileSync(new URL(`./fixtures/${name}.svg`, import.meta.url)));
  const flows = new Set(['sign-in', 'sign-out', 'profile', 'missing-shots']);
  const sides = new Set(['a', 'b', 'diff', 'overlay']);
  await page.route('**/*', async route => {
    const url = new URL(route.request().url());
    const parts = url.pathname.split('/').filter(Boolean);
    if (url.origin !== 'http://127.0.0.1:4971' || route.request().method() !== 'GET') {
      unexpected.push(`${route.request().method()} ${url.origin}${url.pathname}`);
      return route.fulfill({ status: 501, body: 'Unexpected origin or method' });
    }
    if (!url.pathname.startsWith('/api/')) return route.continue();
    if (url.pathname === '/api/queue') return route.fulfill({ json: { items: [], empty: 'no-runs' } });
    if (url.pathname === '/api/pairs') return route.fulfill({ json: { items: [] } });
    if (url.pathname === '/api/suites') return route.fulfill({ json: suitesFixture() });
    if (url.pathname === '/api/suites/example/gallery' || url.pathname === '/api/suites/example/builds/build-one/gallery') return route.fulfill({ json: galleryFixture() });
    if (parts[1] === 'pairs' && parts[2] === 'example-candidate' && flows.has(parts[3]) && parts[4] === 'pinned-run' && parts[5] === 'pinned-reference') {
      if (parts.length === 6) return route.fulfill({ json: { summary: pairFixture(parts[3]) } });
      if (parts.length === 9 && parts[6] === 'shots' && sides.has(parts[7]) && parts[8] === parts[3] && parts[3] !== 'missing-shots') {
        if (options.brokenImage && parts[3] === 'sign-in' && parts[7] === 'b') return route.fulfill({ status: 404, body: 'Synthetic missing image' });
        return route.fulfill({ contentType: 'image/svg+xml', body: images[parts[7] === 'a' ? 0 : 1] });
      }
    }
    unexpected.push(url.pathname);
    return route.fulfill({ status: 501, body: 'Unmocked API route' });
  });
  return unexpected;
}
