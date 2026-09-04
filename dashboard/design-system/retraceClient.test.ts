import { afterEach, describe, expect, it, vi } from 'vitest';
import { createRetraceClient, listRetraceInstances } from './retraceClient';

function fakeResponse(body: unknown, status = 200) {
  return {
    ok: status >= 200 && status < 300,
    status,
    statusText: status === 200 ? 'OK' : 'Error',
    text: () => Promise.resolve(JSON.stringify(body)),
  };
}

/** Captures the one request the call under test makes. */
function captureFetch(response = fakeResponse({ ok: true })) {
  const calls: { url: string; init?: RequestInit }[] = [];
  const mock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    calls.push({ url: typeof input === 'string' ? input : input.toString(), init });
    return Promise.resolve(response);
  });
  vi.stubGlobal('fetch', mock);
  return calls;
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

const client = createRetraceClient('/api');

describe('shotUrl', () => {
  it('builds the four comparison panes', () => {
    expect(client.shotUrl('web', 'search', 'diff', 'results')).toBe('/api/shots/web/search/diff/results');
  });

  it('throws rather than building a URL for a side the summary never wrote', () => {
    // The empty string means images.<side> was "" — the side was never
    // written, and `/api/shots/web/search/diff/` would 404 as a mystery.
    //
    // What this throw does NOT do is make the mistake survivable. Every caller
    // builds an `src` during render, and a render-phase throw does not reach
    // useAsync's error state — there is no error boundary under dashboard/, so
    // React unmounts the root and the reviewer gets a blank page. The
    // component-side guard is what keeps that from happening; see
    // ShotCompare.test.tsx, which drives the exact JSON summary.go emits for a
    // missing checkpoint.
    expect(() => client.shotUrl('web', 'search', 'diff', '')).toThrow(/no diff-side image/);
  });
});

describe('itemAtRun', () => {
  it('hits the run-scoped item route rather than the plain (latest) one', async () => {
    const calls = captureFetch(fakeResponse({ summary: {} }));
    await client.itemAtRun('web', 'search', '20260821T101000Z-bbbbbbb');

    expect(calls).toHaveLength(1);
    expect(calls[0].url).toBe('/api/queue/web/search/runs/20260821T101000Z-bbbbbbb');
  });
});

describe('shotUrlAtRun', () => {
  it('builds a run-scoped shot URL, distinct from the plain (latest) route', () => {
    expect(client.shotUrlAtRun('web', 'search', '20260821T101000Z-bbbbbbb', 'diff', 'results')).toBe(
      '/api/shots/web/search/runs/20260821T101000Z-bbbbbbb/diff/results',
    );
  });

  it('throws rather than building a URL for a side the summary never wrote', () => {
    expect(() => client.shotUrlAtRun('web', 'search', '20260821T101000Z-bbbbbbb', 'diff', '')).toThrow(
      /no diff-side image/,
    );
  });
});

describe('instance scoping', () => {
  it('carries no ?instance= param when the client was built with none — the single-repo/retrace-ui case', async () => {
    const calls = captureFetch(fakeResponse({ items: [], empty: '' }));
    await client.queue();
    expect(calls[0].url).toBe('/api/queue');
  });

  it('adds ?instance= to a request with no other query params', async () => {
    const scoped = createRetraceClient('/api/retrace', 'web');
    const calls = captureFetch(fakeResponse({ summary: {} }));
    await scoped.item('web', 'checkout');
    expect(calls[0].url).toBe('/api/retrace/queue/web/checkout?instance=web');
  });

  it('merges ?instance= alongside an existing filter query string', async () => {
    const scoped = createRetraceClient('/api/retrace', 'web');
    const calls = captureFetch(fakeResponse({ items: [], empty: '' }));
    await scoped.queue({ source: 'ci' });
    const url = new URL(calls[0].url, 'http://x');
    expect(url.pathname).toBe('/api/retrace/queue');
    expect(url.searchParams.get('source')).toBe('ci');
    expect(url.searchParams.get('instance')).toBe('web');
  });

  it('carries the instance on URL-builder methods too (shotUrl/videoUrl/reportUrl), since those become <img>/<video>/<a> attributes the server also resolves per-instance', () => {
    const scoped = createRetraceClient('/api/retrace', 'web');
    expect(scoped.shotUrl('a', 'b', 'diff', 'c')).toBe('/api/retrace/shots/a/b/diff/c?instance=web');
    expect(scoped.videoUrl('a', 'b', 'c.webm')).toBe('/api/retrace/videos/a/b/c.webm?instance=web');
    expect(scoped.reportUrl('a', 'b')).toBe('/api/retrace/report/a/b/?instance=web');
  });
});

describe('syncCandidates', () => {
  it('carries no workflows param when none are given', async () => {
    const calls = captureFetch(fakeResponse({ candidates: [] }));
    await client.syncCandidates('acme/widgets');
    const url = new URL(calls[0].url, 'http://x');
    expect(url.searchParams.has('workflows')).toBe(false);
  });

  it('joins the workflows filter as a comma-separated query param, matching the server\'s splitCSV', async () => {
    const calls = captureFetch(fakeResponse({ candidates: [] }));
    await client.syncCandidates('acme/widgets', { workflows: ['e2e', 'unit'] });
    const url = new URL(calls[0].url, 'http://x');
    expect(url.searchParams.get('workflows')).toBe('e2e,unit');
  });

  it('omits workflows for an empty array rather than sending an empty param', async () => {
    const calls = captureFetch(fakeResponse({ candidates: [] }));
    await client.syncCandidates('acme/widgets', { workflows: [] });
    const url = new URL(calls[0].url, 'http://x');
    expect(url.searchParams.has('workflows')).toBe(false);
  });
});

describe('syncBranches', () => {
  it('hits the branches endpoint with the given repo', async () => {
    const calls = captureFetch(fakeResponse({ branches: [] }));
    await client.syncBranches('acme/widgets');
    const url = new URL(calls[0].url, 'http://x');
    expect(url.pathname).toBe('/api/sync/branches');
    expect(url.searchParams.get('repo')).toBe('acme/widgets');
  });

  it('joins the workflows filter as a comma-separated query param', async () => {
    const calls = captureFetch(fakeResponse({ branches: [] }));
    await client.syncBranches('acme/widgets', { workflows: ['Retrace *'] });
    const url = new URL(calls[0].url, 'http://x');
    expect(url.searchParams.get('workflows')).toBe('Retrace *');
  });

  it('carries since through to the query string', async () => {
    const calls = captureFetch(fakeResponse({ branches: [] }));
    await client.syncBranches('acme/widgets', { since: '30d' });
    const url = new URL(calls[0].url, 'http://x');
    expect(url.searchParams.get('since')).toBe('30d');
  });
});

describe('listRetraceInstances', () => {
  it('fetches {basePath}/instances', async () => {
    const calls = captureFetch(fakeResponse({ instances: [{ key: 'web', label: 'Web' }] }));
    const result = await listRetraceInstances('/api/retrace');
    expect(calls[0].url).toBe('/api/retrace/instances');
    expect(result.instances).toEqual([{ key: 'web', label: 'Web' }]);
  });
});

describe('pairs', () => {
  it('lists persisted cross-app diffs', async () => {
    const calls = captureFetch(fakeResponse({ pairs: [] }));
    await client.pairs();
    expect(calls[0].url).toBe('/api/pairs');
  });

  it('fetches one pairing by appB/flowB/runB/pairId', async () => {
    const calls = captureFetch(fakeResponse({ summary: {} }));
    await client.pair('mobile', 'checkout', '20260904T120000Z-abc1234', 'web__reference');
    expect(calls[0].url).toBe('/api/pairs/mobile/checkout/20260904T120000Z-abc1234/web__reference');
  });

  it('builds a pairing shot URL for one of the four comparison panes', () => {
    expect(client.pairShotUrl('mobile', 'checkout', '20260904T120000Z-abc1234', 'web__reference', 'overlay', 'home')).toBe(
      '/api/pairs/mobile/checkout/20260904T120000Z-abc1234/web__reference/shots/overlay/home',
    );
  });

  it('throws rather than building a pairing shot URL for a side the summary never wrote', () => {
    expect(() =>
      client.pairShotUrl('mobile', 'checkout', '20260904T120000Z-abc1234', 'web__reference', 'diff', ''),
    ).toThrow(/no diff-side image/);
  });
});
