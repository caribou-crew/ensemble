import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import App from './App';
import type { CaptureTrust, Counts, Item, Summary } from './api/types';

// --- fixtures -----------------------------------------------------------

const okTrust: CaptureTrust = { status: 'ok', summary: 'capture looks complete' };
const zeroCounts: Counts = {
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
};

const item = (app: string, flow: string, over: Partial<Item> = {}): Item => ({
  app,
  flow,
  verdict: 'pass',
  score: 0,
  runId: '20260821T101000Z-bbbbbbb',
  counts: zeroCounts,
  capture: { a: okTrust, b: okTrust },
  gates: [],
  ...over,
});

/**
 * Two rows that need attention and two that do not — worst first, the order
 * the server sends.
 *
 * TWO of each, deliberately. QueueList renders `score > 0` and collapses
 * `score === 0`, so a fixture with a single group cannot detect a `j` that
 * walks the unfiltered array, and a single group is the natural fixture to
 * write. With two visible rows, `j` pressed four times has somewhere wrong to
 * go and something to prove.
 */
const QUEUE: Item[] = [
  item('web', 'cart', { verdict: 'failed', score: 1100, gates: ['status 500 on GET /cart'] }),
  item('web', 'search', { verdict: 'changed', score: 2 }),
  item('admin', 'login'),
  item('web', 'login'),
];

const summary = (over: Partial<Summary> = {}): Summary => ({
  schema: 'retrace-diff/1',
  app: 'web',
  flow: 'search',
  verdict: 'changed',
  a: {
    runId: 'reference',
    kind: 'bundle',
    dir: '/runs/a',
    manifest: {
      schema: 'retrace-run/1',
      app: 'web',
      flow: 'search',
      runId: 'reference',
      mode: 'standalone',
      git: { sha: 'deadbee', branch: 'main', dirty: false },
      startedAt: '2026-08-21T10:00:00Z',
      finishedAt: '2026-08-21T10:00:05Z',
      checkpoints: [],
      groups: [],
      capture: okTrust,
      wire: { calls: 1, recorded: true },
      test: { command: 'go test', exitCode: 0, durationMs: 12 },
      env: { go: '1.25', platform: 'darwin', retrace: 'test' },
    },
  },
  b: {
    runId: '20260821T101000Z-bbbbbbb',
    kind: 'run',
    dir: '/runs/b',
    manifest: {
      schema: 'retrace-run/1',
      app: 'web',
      flow: 'search',
      runId: '20260821T101000Z-bbbbbbb',
      mode: 'standalone',
      git: { sha: 'deadbee', branch: 'main', dirty: false },
      startedAt: '2026-08-21T10:10:00Z',
      finishedAt: '2026-08-21T10:10:05Z',
      checkpoints: [],
      groups: [],
      capture: okTrust,
      wire: { calls: 1, recorded: true },
      test: { command: 'go test', exitCode: 0, durationMs: 12 },
      env: { go: '1.25', platform: 'darwin', retrace: 'test' },
    },
  },
  quarantined: [],
  checkpoints: [],
  wire: { paired: [], missing: [], extra: [] },
  sections: [],
  hops: { serviceCounts: [], newRoutes: [], goneRoutes: [], hopRequireConfigured: false },
  unexpectedStatuses: [],
  perf: { status: 'unset', measuredMs: 0, budgetMs: 0 },
  conformance: [],
  openApiConfigured: false,
  capture: { a: okTrust, b: okTrust },
  counts: zeroCounts,
  gates: [],
  budgets: [],
  ...over,
});

// --- harness ------------------------------------------------------------

interface Call {
  url: string;
  method: string;
}

/**
 * A fake `fetch` that answers the review server's REST surface. The three
 * verbs answer whatever `posts` says, and every call is recorded — so a test
 * can assert which ENDPOINT a key hit, which is the thing the surviving
 * mutation (api.accept → api.reject on the `a` key) changed while the notice
 * went on saying "accepted … as the new reference".
 */
function stubServer(opts: {
  queue?: Item[];
  empty?: string;
  item?: Summary;
  posts?: Record<string, unknown>;
} = {}) {
  const calls: Call[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === 'string' ? input : String(input);
      const method = init?.method ?? 'GET';
      calls.push({ url, method });

      let body: unknown = { ok: true };
      if (url === '/api/queue') {
        body = { items: opts.queue ?? QUEUE, empty: opts.empty ?? '' };
      } else if (method === 'GET') {
        body = { summary: opts.item ?? summary() };
      } else {
        const verb = url.split('/').pop() ?? '';
        body = opts.posts?.[verb] ?? { ok: true };
      }
      return Promise.resolve({
        ok: true,
        status: 200,
        statusText: 'OK',
        text: () => Promise.resolve(JSON.stringify(body)),
      });
    }),
  );
  return calls;
}

let container: HTMLDivElement;
let root: Root;

beforeEach(() => {
  window.history.replaceState({}, '', '/');
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
});
afterEach(() => {
  act(() => root.unmount());
  container.remove();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

async function mount() {
  await act(async () => {
    root.render(<App />);
  });
}

async function press(key: string) {
  await act(async () => {
    window.dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true }));
  });
}

const text = () => container.textContent ?? '';
const selectedRow = () =>
  container.querySelector('.queue-row--selected .queue-row__flow')?.textContent ?? null;
const notice = () => container.querySelector('.notice')?.textContent ?? null;
const renderedFlows = () =>
  Array.from(container.querySelectorAll('.queue-row__flow')).map((el) => el.textContent);

// --- the keyboard dispatch ---------------------------------------------

/**
 * F3. `App` had no test at all — not the keyboard dispatch, not any of the
 * three verbs' wiring, not one line of their notice text. The mutation that
 * survived the whole suite was `api.accept` → `api.reject` on the `a` key:
 * 28 tests green, a different filesystem mutation performed, and the UI still
 * reporting "accepted … as the new reference".
 */
describe('the keyboard dispatch', () => {
  it('never moves the selection off the rows that are on screen', async () => {
    stubServer();
    await mount();
    expect(renderedFlows()).toEqual(['web/cart', 'web/search']);

    // Four presses over two visible rows. Walking the unfiltered server list
    // would land on admin/login and then web/login — rows inside a collapsed
    // disclosure, so NOTHING on screen is selected, the key looks like a
    // no-op, and `enter` opens a flow the reviewer never saw.
    const seen: (string | null)[] = [];
    for (let i = 0; i < 4; i++) {
      await press('j');
      seen.push(selectedRow());
    }
    expect(seen).toEqual(['web/cart', 'web/search', 'web/search', 'web/search']);
    for (const s of seen) {
      expect(renderedFlows()).toContain(s);
    }

    // And back up, symmetrically — k must not walk off the top either.
    await press('k');
    expect(selectedRow()).toBe('web/cart');
    await press('k');
    expect(selectedRow()).toBe('web/cart');
  });

  it('reaches the passing rows once the reviewer expands them, and not before', async () => {
    stubServer();
    await mount();
    await press('j');
    await press('j');
    expect(selectedRow()).toBe('web/search');

    const disclosure = container.querySelector('.queue__disclosure') as HTMLButtonElement;
    expect(disclosure.textContent).toContain('2 passing');
    await act(async () => {
      disclosure.dispatchEvent(new MouseEvent('click', { bubbles: true }));
    });

    await press('j');
    expect(selectedRow()).toBe('admin/login');
    expect(renderedFlows()).toContain('admin/login');
  });

  it('opens the selected flow on enter and comes back on esc', async () => {
    stubServer();
    await mount();
    await press('j');
    await press('Enter');
    expect(container.querySelector('.item')).not.toBeNull();
    expect(text()).toContain('web/cart');

    await press('Escape');
    expect(container.querySelector('.item')).toBeNull();
    expect(container.querySelector('.queue')).not.toBeNull();
  });
});

// --- the three verbs ----------------------------------------------------

/** Opens web/search on the item screen, which is where the verbs fire from. */
async function openAFlow(calls: Call[]) {
  await press('j');
  await press('j');
  await press('Enter');
  calls.length = 0;
  return calls;
}

describe('the three verbs', () => {
  it('the a key POSTs to accept — the endpoint, not just the notice', async () => {
    const calls = stubServer({
      posts: {
        accept: {
          ok: true,
          bundle: {
            dir: '.retrace/refs/web/search',
            files: ['manifest.json'],
            bytes: 4096,
            runId: '20260821T101000Z-bbbbbbb',
            captureStatus: 'ok',
            unmatchedMasks: [],
          },
        },
      },
    });
    await mount();
    await openAFlow(calls);

    await press('a');
    const posts = calls.filter((c) => c.method === 'POST');
    expect(posts).toHaveLength(1);
    expect(posts[0].url).toBe('/api/queue/web/search/accept');
    expect(notice()).toMatch(/accepted web\/search as the new reference/);
  });

  it('the r key POSTs to reject, and says what it wrote', async () => {
    const calls = stubServer({
      posts: {
        reject: {
          ok: true,
          repro: {
            dir: '.retrace/repro/web__search__b',
            files: ['manifest.json', 'summary.json'],
            runId: '20260821T101000Z-bbbbbbb',
          },
        },
      },
    });
    await mount();
    await openAFlow(calls);

    await press('r');
    const posts = calls.filter((c) => c.method === 'POST');
    expect(posts).toHaveLength(1);
    expect(posts[0].url).toBe('/api/queue/web/search/reject');
    expect(notice()).toContain('.retrace/repro/web__search__b');
  });

  it('the u key opens the rule picker for the selected field, and nothing else does', async () => {
    const withField = summary({
      sections: [
        {
          name: 'checkout',
          counts: { changed: 1 },
          entries: [
            {
              method: 'GET',
              normalizedPath: '/orders/{id}',
              seqA: 1,
              seqB: 1,
              posA: 0,
              posB: 0,
              moved: false,
              truncated: false,
              classes: ['changed'],
              bodyDiff: [{ scope: 'resp', path: 'placedAt', type: 'changed', a: 'T1', b: 'T2' }],
              bodyTolerated: [],
              bodyViolations: [],
              bodyIgnored: [],
              orderingChanges: [],
              headerDiff: [],
            },
          ],
        },
      ],
    });
    const calls = stubServer({ item: withField });
    await mount();
    await openAFlow(calls);

    // Nothing is selected yet, so `u` has no field to write a rule for.
    await press('u');
    expect(container.querySelector('.picker')).toBeNull();

    await act(async () => {
      (container.querySelector('.wire-row__toggle') as HTMLButtonElement).dispatchEvent(
        new MouseEvent('click', { bubbles: true }),
      );
    });
    await act(async () => {
      (container.querySelector('.wire-field__button') as HTMLButtonElement).dispatchEvent(
        new MouseEvent('click', { bubbles: true }),
      );
    });
    await press('u');

    const picker = container.querySelector('.picker');
    expect(picker).not.toBeNull();
    expect(picker?.textContent).toContain('placedAt');
    // And no rule has been written by merely opening it.
    expect(calls.filter((c) => c.method === 'POST')).toHaveLength(0);
  });

  it('refuses to accept or reject from the QUEUE screen, where the reviewer cannot see the run', async () => {
    // Both verbs are filesystem mutations, and ungated they fired from the
    // queue against whatever ?app=&flow= happened to hold — including a
    // selection that had walked onto a collapsed row.
    const calls = stubServer();
    await mount();
    await press('j'); // a row IS selected; ?app=&flow= are populated
    expect(selectedRow()).toBe('web/cart');
    calls.length = 0;

    await press('a');
    await press('r');
    expect(calls.filter((c) => c.method === 'POST')).toEqual([]);
    expect(notice()).toBeNull();
  });
});

// --- what the verbs SAY -------------------------------------------------

/**
 * F1 and D3, and the same argument in both: the response carries the field
 * that stops a reassuring reading, and the UI threw it away. Each is pinned
 * with TWO arms whose notices must differ — pinning only the clean arm is the
 * value costume these seams invite, because the clean arm is the fixture you
 * already have.
 */
describe('what the verbs report', () => {
  async function acceptWith(bundle: Record<string, unknown>): Promise<string> {
    const calls = stubServer({ posts: { accept: { ok: true, bundle } } });
    await mount();
    await openAFlow(calls);
    await press('a');
    const said = notice();
    expect(said).not.toBeNull();
    return said as string;
  }

  const CLEAN = {
    dir: '.retrace/refs/web/search',
    files: ['manifest.json'],
    bytes: 4096,
    runId: '20260821T101000Z-bbbbbbb',
    captureStatus: 'ok',
    unmatchedMasks: [] as string[],
  };

  it('says that a promotion off a DEGRADED capture was one', async () => {
    const clean = await acceptWith(CLEAN);
    act(() => root.unmount());
    container.remove();
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
    window.history.replaceState({}, '', '/');
    const degraded = await acceptWith({ ...CLEAN, captureStatus: 'degraded' });

    expect(degraded).not.toBe(clean);
    expect(degraded).toMatch(/degraded/);
    expect(degraded).toMatch(/inherits that doubt/);
    expect(clean).not.toMatch(/degraded/);
  });

  it('says that a mask entry redacted NOTHING — the one that ends with pixels in git', async () => {
    const said = await acceptWith({ ...CLEAN, unmatchedMasks: ['receipt'] });
    expect(said).toMatch(/receipt/);
    expect(said).toMatch(/redacted NOTHING|matched no checkpoint/);
    // refs.go keeps this a warning rather than a refusal precisely because
    // "a typo silently redacting nothing is the one that ends with pixels in
    // git" — and these shots are about to be COMMITTED.
    expect(said).toMatch(/WARNING/);
  });

  it("says a repro bundle does not explain the rejection when the server said it doesn't", async () => {
    const repro = {
      dir: '.retrace/repro/web__search__b',
      files: ['manifest.json'],
      runId: '20260821T101000Z-bbbbbbb',
    };
    const calls = stubServer({ posts: { reject: { ok: true, repro } } });
    await mount();
    await openAFlow(calls);
    await press('r');
    const clean = notice();

    act(() => root.unmount());
    container.remove();
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
    window.history.replaceState({}, '', '/');

    const calls2 = stubServer({
      posts: {
        reject: { ok: true, repro, warning: 'no summary.json in this repro bundle — no reference' },
      },
    });
    await mount();
    await openAFlow(calls2);
    await press('r');
    const warned = notice();

    expect(warned).not.toBe(clean);
    expect(warned).toMatch(/no summary.json/);
    // In place of the unqualified sentence, not beside it: a reviewer who
    // reads "repro bundle written to <dir>" believes they have a bundle that
    // explains the rejection. They have a directory.
    expect(warned).toMatch(/does NOT explain the rejection/);
    expect(clean).not.toMatch(/does NOT explain/);
  });
});

// --- the item screen ----------------------------------------------------

describe('the item screen', () => {
  it('banners a broken capture — the second of the two report surfaces', async () => {
    const calls = stubServer({
      item: summary({
        capture: { a: okTrust, b: { status: 'broken', summary: 'the proxy died 12s in' } },
      }),
    });
    await mount();
    await openAFlow(calls);
    const banner = container.querySelector('.capture-banner');
    expect(banner).not.toBeNull();
    expect(banner?.textContent).toContain('this run');
    expect(banner?.textContent).toContain('broken');
    expect(banner?.textContent).not.toContain('reference');
  });

  it('paints a quarantined flow as a call for attention, the same colour the queue row uses', async () => {
    const calls = stubServer({
      item: summary({
        verdict: 'quarantined',
        quarantined: [{ side: 'b', reason: 'the proxy was down for 40s' }],
      }),
    });
    await mount();
    await openAFlow(calls);
    const badge = Array.from(container.querySelectorAll('.ds-badge')).find(
      (b) => b.textContent === 'quarantined',
    );
    expect(badge).toBeDefined();
    expect(badge!.className).not.toContain('ds-badge--neutral');
    expect(badge!.className).toContain('ds-badge--amber');
    expect(text()).toContain('This flow was not compared');
  });
});
