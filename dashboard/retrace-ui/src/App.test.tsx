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
  suppressions: [],
  gates: [],
  budgets: [],
  unmeasuredGates: [],
  // Spelled out rather than left optional. `triage` is required on the Go
  // side — diff.Build sets it at every exit — so a fixture that omitted it
  // would be a shape the server cannot produce, and typing the field as
  // optional here to make the fixture compile would have hidden that.
  triage: {
    label: 'client-behavior',
    rule: 'wire-moved',
    signals: { pixel: false, wire: true, hop: false, spec: false, capture: false },
  },
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
  runs?: unknown[];
  evidence?: { videos: string[]; hasReport: boolean };
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
      if (url === '/api/queue' || url.startsWith('/api/queue?')) {
        body = { items: opts.queue ?? QUEUE, empty: opts.empty ?? '' };
      } else if (url.startsWith('/api/evidence/')) {
        body = opts.evidence ?? { videos: [], hasReport: false };
      } else if (method === 'GET' && /\/runs$/.test(url.split('?')[0])) {
        // The runs-list for a surface. Default to a single run (the
        // summary's B run) so openAFlow can drill straight through it.
        const sum = opts.item ?? summary();
        body = {
          runs: opts.runs ?? [
            {
              runId: sum.b.runId || '20260821T101000Z-bbbbbbb',
              verdict: sum.verdict,
              when: sum.b.manifest.finishedAt,
              counts: sum.counts,
              gates: sum.gates,
            },
          ],
        };
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
// app and flow are separate columns now, not a joined "app/flow" string —
// rebuild the same key from the two cells so the rest of this suite (which
// asserts on "web/cart"-shaped strings) doesn't need to change shape.
const rowKey = (row: Element): string => {
  const app = row.querySelector('.queue-row__app')?.textContent ?? '';
  const flow = row.querySelector('.queue-row__flowname')?.textContent ?? '';
  return `${app}/${flow}`;
};
const selectedRow = () => {
  const row = container.querySelector('.queue-row--selected');
  return row ? rowKey(row) : null;
};
const notice = () => container.querySelector('.notice')?.textContent ?? null;
const renderedFlows = () =>
  Array.from(container.querySelectorAll('tr.queue-row')).map((row) => rowKey(row));

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
    // Needs-attention rows first, then passing — one flat table, all four
    // rows on screen at once (there is no collapsed-passing disclosure any
    // more; see RetraceQueueList.visibleRows).
    expect(renderedFlows()).toEqual(['web/cart', 'web/search', 'admin/login', 'web/login']);

    // Five presses over four visible rows: the fifth has nowhere left to go,
    // so the selection must stay on the last row rather than walking off the
    // end of the rendered set.
    const seen: (string | null)[] = [];
    for (let i = 0; i < 5; i++) {
      await press('j');
      seen.push(selectedRow());
    }
    expect(seen).toEqual(['web/cart', 'web/search', 'admin/login', 'web/login', 'web/login']);
    for (const s of seen) {
      expect(renderedFlows()).toContain(s);
    }

    // And back up, symmetrically — k must not walk off the top either.
    for (let i = 0; i < 5; i++) {
      await press('k');
    }
    expect(selectedRow()).toBe('web/cart');
  });

  it('reaches the passing rows on plain j presses, with no disclosure to expand first', async () => {
    stubServer();
    await mount();

    // Passing rows render inline from the start — nothing to click open.
    expect(container.querySelector('.queue__disclosure')).toBeNull();
    expect(renderedFlows()).toContain('admin/login');

    await press('j');
    await press('j');
    await press('j');
    expect(selectedRow()).toBe('admin/login');
  });

  it('drills queue -> runs -> detail on enter/click and steps back up on esc', async () => {
    stubServer();
    await mount();
    // Level 1 -> highlight web/cart, Enter opens its runs list.
    await press('j');
    await press('Enter');
    expect(container.querySelector('.item')).toBeNull();
    expect(container.querySelector('.queue-table')).not.toBeNull();
    expect(text()).toContain('web/cart'); // the breadcrumb names the surface

    // Level 2 -> click the first run, opening its detail.
    const runOpener = container.querySelector('.queue-row__open') as HTMLElement;
    await act(async () => {
      runOpener.dispatchEvent(new MouseEvent('click', { bubbles: true }));
    });
    expect(container.querySelector('.item')).not.toBeNull();

    // esc steps up exactly one level each time: detail -> runs list -> queue.
    await press('Escape');
    expect(container.querySelector('.item')).toBeNull();
    expect(container.querySelector('.breadcrumb-bar')).not.toBeNull();
    expect(container.querySelector('.breadcrumb__back')).not.toBeNull();
    await press('Escape');
    // Back at the queue root: the breadcrumb bar stays in the header (it's
    // the persistent nav strip now), but its back control and trail
    // segments are gone — just the "retrace review" root remains.
    expect(container.querySelector('.breadcrumb__back')).toBeNull();
    expect(container.querySelector('.breadcrumb__sep')).toBeNull();
    expect(container.querySelector('.queue')).not.toBeNull();
    expect(renderedFlows()).toContain('web/cart');
  });

  it('keeps a persistent header nav that jumps straight back to the top level from any depth', async () => {
    stubServer();
    await mount();
    const header = container.querySelector('.app-header') as HTMLElement;
    expect(header.textContent).toContain('retrace review');
    // At the queue root there's nowhere to go back to, so no back control.
    expect(header.querySelector('.breadcrumb__back')).toBeNull();

    // Drill all the way to a run's detail screen.
    await press('j');
    await press('Enter');
    const runOpener = container.querySelector('.queue-row__open') as HTMLElement;
    await act(async () => {
      runOpener.dispatchEvent(new MouseEvent('click', { bubbles: true }));
    });
    expect(container.querySelector('.item')).not.toBeNull();

    // The header still shows the full trail, and its root segment is a
    // single click back to the queue — no need to step up one level at a
    // time via the now-removed standalone breadcrumb bar in <main>.
    expect(header.textContent).toContain('retrace review');
    const root = header.querySelector('.breadcrumb__link') as HTMLButtonElement;
    expect(root.textContent).toBe('retrace review');
    await act(async () => {
      root.dispatchEvent(new MouseEvent('click', { bubbles: true }));
    });
    expect(container.querySelector('.item')).toBeNull();
    expect(container.querySelector('.queue')).not.toBeNull();
  });
});

// --- the three verbs ----------------------------------------------------

/** Opens web/search on the item screen, which is where the verbs fire from. */
async function openAFlow(calls: Call[]) {
  // Level 1 -> highlight web/search and open it into its runs list.
  await press('j');
  await press('j');
  await press('Enter');
  // Level 2 -> open the first (and, by default, only) run into its detail,
  // where the verbs fire from.
  const runOpener = container.querySelector('.queue-row__open') as HTMLElement | null;
  if (runOpener) {
    await act(async () => {
      runOpener.dispatchEvent(new MouseEvent('click', { bubbles: true }));
    });
  }
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
              headerIgnored: [],
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
      (container.querySelector('.wire-row__toggle') as HTMLElement).dispatchEvent(
        new MouseEvent('click', { bubbles: true }),
      );
    });
    await act(async () => {
      (container.querySelector('.wire-field') as HTMLElement).dispatchEvent(
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

  it('the m key opens the redact picker for the selected field, pre-filled with its leaf name', async () => {
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
              bodyDiff: [{ scope: 'resp', path: 'account.number', type: 'changed', a: '1234', b: '5678' }],
              bodyTolerated: [],
              bodyViolations: [],
              bodyIgnored: [],
              orderingChanges: [],
              headerDiff: [],
              headerIgnored: [],
            },
          ],
        },
      ],
    });
    const calls = stubServer({ item: withField });
    await mount();
    await openAFlow(calls);

    // Nothing is selected yet, so `m` has no field to write a redaction for.
    await press('m');
    expect(container.querySelector('.picker')).toBeNull();

    await act(async () => {
      (container.querySelector('.wire-row__toggle') as HTMLElement).dispatchEvent(
        new MouseEvent('click', { bubbles: true }),
      );
    });
    await act(async () => {
      (container.querySelector('.wire-field') as HTMLElement).dispatchEvent(
        new MouseEvent('click', { bubbles: true }),
      );
    });
    await press('m');

    const picker = container.querySelector('.picker');
    expect(picker).not.toBeNull();
    expect(picker?.textContent).toContain('account.number');
    // Pre-filled with the LEAF key ("number"), which is what config's
    // RedactKeyRules actually matches — not the full dotted path.
    const fieldInput = Array.from(picker!.querySelectorAll('input')).find((i) => i.value === 'number');
    expect(fieldInput).toBeDefined();
    // And no rule has been written by merely opening it.
    expect(calls.filter((c) => c.method === 'POST')).toHaveLength(0);
  });

  it('the m key POSTs to redact — the endpoint, with the edited field/mode/why', async () => {
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
              bodyDiff: [{ scope: 'resp', path: 'accountNumber', type: 'changed', a: '1234', b: '5678' }],
              bodyTolerated: [],
              bodyViolations: [],
              bodyIgnored: [],
              orderingChanges: [],
              headerDiff: [],
              headerIgnored: [],
            },
          ],
        },
      ],
    });
    const calls = stubServer({ item: withField, posts: { redact: { ok: true } } });
    await mount();
    await openAFlow(calls);
    await act(async () => {
      (container.querySelector('.wire-row__toggle') as HTMLElement).dispatchEvent(
        new MouseEvent('click', { bubbles: true }),
      );
    });
    await act(async () => {
      (container.querySelector('.wire-field') as HTMLElement).dispatchEvent(
        new MouseEvent('click', { bubbles: true }),
      );
    });
    await press('m');

    const select = container.querySelector('.picker select') as HTMLSelectElement;
    const setter = Object.getOwnPropertyDescriptor(window.HTMLSelectElement.prototype, 'value')!.set!;
    await act(async () => {
      setter.call(select, 'encrypt');
      select.dispatchEvent(new Event('change', { bubbles: true }));
    });

    const confirm = Array.from(container.querySelectorAll('.picker button')).find(
      (b) => b.textContent === 'write the rule',
    );
    await act(async () => {
      confirm!.dispatchEvent(new MouseEvent('click', { bubbles: true }));
    });

    const posts = calls.filter((c) => c.method === 'POST');
    expect(posts).toHaveLength(1);
    expect(posts[0].url).toBe('/api/queue/web/search/redact');
    expect(notice()).toMatch(/wrote a redaction rule for "accountNumber" \(encrypt\)/);
    expect(container.querySelector('.picker')).toBeNull();
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

describe('a notice belongs to the flow it was produced for', () => {
  it('is cleared when the reviewer moves to another flow', async () => {
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
    expect(notice()).toMatch(/accepted web\/search/);

    // Back to the queue (run -> surface -> queue, one level per esc) and
    // onto a different row. The message is about web/search and must not
    // sit above web/cart.
    await press('Escape');
    await press('Escape');
    await press('k');
    expect(selectedRow()).toBe('web/cart');
    expect(notice()).toBeNull();
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

  it('paints a not-compared flow as a call for attention, the same colour the queue row uses', async () => {
    const calls = stubServer({
      item: summary({
        verdict: 'quarantined',
        quarantined: [{ side: 'b', reason: 'the proxy was down for 40s' }],
      }),
    });
    await mount();
    await openAFlow(calls);
    const badge = Array.from(container.querySelectorAll('.ds-badge')).find(
      (b) => b.textContent === 'not compared',
    );
    expect(badge).toBeDefined();
    expect(badge!.className).not.toContain('ds-badge--neutral');
    expect(badge!.className).toContain('ds-badge--amber');
    expect(text()).toContain('This flow was not compared');
  });

  it('shows the captured screenshots for a not-compared run instead of nothing', async () => {
    // The per-checkpoint diff can't run for a quarantined verdict, but the
    // run's manifest still recorded whatever the app rendered — the
    // reviewer should be able to look at it rather than seeing empty planes.
    const calls = stubServer({
      item: summary({
        verdict: 'quarantined',
        quarantined: [{ side: 'b', reason: 'the proxy was down for 40s' }],
        b: {
          ...summary().b,
          manifest: {
            ...summary().b.manifest,
            checkpoints: [
              { name: 'cart', file: 'cart.png', width: 100, height: 100, at: '2026-08-21T10:10:01Z' },
            ],
          },
        },
      }),
    });
    await mount();
    await openAFlow(calls);
    expect(text()).toContain('captured screenshots');
    const img = container.querySelector('.item__capture-img') as HTMLImageElement | null;
    expect(img).not.toBeNull();
    expect(img!.alt).toContain('cart');
  });

  it('renders a back control on the item screen that steps up to the runs list', async () => {
    const calls = stubServer();
    await mount();
    await openAFlow(calls);
    const back = container.querySelector('.item__back') as HTMLButtonElement | null;
    expect(back).not.toBeNull();
    await act(async () => {
      back!.dispatchEvent(new MouseEvent('click', { bubbles: true }));
    });
    // One level up: the runs list for this surface, not all the way to the
    // queue — same "step up exactly one level" contract as esc.
    expect(container.querySelector('.item')).toBeNull();
    expect(container.querySelector('.breadcrumb-bar')).not.toBeNull();
    expect(container.querySelector('.queue-table')).not.toBeNull();
  });

  /**
   * F-1. `diff.budgetsOf` emits no budget row for a plane it could not
   * measure — by the same code path as a plane nobody gated — so the
   * absence of a row means one of two opposite things and only the config
   * separates them. This screen rendered the budgets section on
   * `budgets.length > 0` alone, so the case where EVERY gated plane was
   * unmeasurable rendered nothing at all: the reviewer saw a page with no
   * budgets section and read it as "this project gates nothing".
   *
   * The static HTML export said the honest sentence; the CLI, `--json` and
   * this screen did not. One signal on the Summary, four consumers.
   */
  it('names a gate that could not be evaluated instead of rendering nothing', async () => {
    const calls = stubServer({
      item: summary({ budgets: [], unmeasuredGates: ['perf', 'pixel'] }),
    });
    await mount();
    await openAFlow(calls);

    const section = container.querySelector('.item__budgets');
    expect(section).not.toBeNull();
    expect(section?.textContent).toContain('perf');
    expect(section?.textContent).toContain('pixel');
    expect(section?.textContent).toContain('not evaluated');
    expect(section?.textContent).toContain('not a gate that passed');

    // Amber, the tone `quarantined` gets — "could not evaluate", not
    // "evaluated and fine". Green here would restate the defect in colour.
    const badges = Array.from(container.querySelectorAll('.item__budgets .ds-badge'));
    expect(badges.map((b) => b.className)).toEqual([
      expect.stringContaining('ds-badge--amber'),
      expect.stringContaining('ds-badge--amber'),
    ]);
  });

  /**
   * The four planes each report WHAT moved; none of them reports whose
   * problem it is, and a reviewer reading them in isolation goes and edits a
   * client that never changed. `triage` is the answer, and `signals` is what
   * lets the reviewer check the answer rather than take it on faith.
   */
  it('says whose problem the flow is, with the evidence behind the label', async () => {
    const calls = stubServer({
      item: summary({
        triage: {
          label: 'stack',
          rule: 'hop-only',
          signals: { pixel: false, wire: false, hop: true, spec: false, capture: false },
        },
      }),
    });
    await mount();
    await openAFlow(calls);

    const el = container.querySelector('.item__triage');
    expect(el).not.toBeNull();
    expect(el?.textContent).toContain('stack');
    expect(el?.textContent).toContain('hop-only');
    expect(el?.textContent).toContain('signals moved: hop');
    // The signals that did NOT move are absent, not listed as false. A vector
    // rendered in full reads as five findings.
    expect(el?.textContent).not.toContain('pixel');
  });

  /**
   * A project's own `triage:` rule may emit any string. Rendering must not
   * switch exhaustively over the built-in labels, or a configured label
   * silently disappears from the one screen a reviewer looks at.
   */
  it('renders a label the built-in table never produces', async () => {
    const calls = stubServer({
      item: summary({
        triage: {
          label: 'seeds',
          rule: 'seed-drift',
          signals: { pixel: false, wire: false, hop: true, spec: false, capture: false },
        },
      }),
    });
    await mount();
    await openAFlow(calls);
    expect(container.querySelector('.item__triage')?.textContent).toContain('seeds');
  });

  it('says nothing about unevaluated gates when every gate ran', async () => {
    const calls = stubServer({
      item: summary({
        budgets: [{ plane: 'pixel', threshold: 5, observed: 0.2, failed: false }],
        unmeasuredGates: [],
      }),
    });
    await mount();
    await openAFlow(calls);
    const section = container.querySelector('.item__budgets');
    expect(section?.textContent).toContain('pixel');
    expect(section?.textContent).not.toContain('not evaluated');
  });

  /**
   * `Gate.observed`/`.threshold` are already on a percent scale (a
   * `budget_pct: 0.1` means 0.1%, not 0.1) — rendering the raw float gave
   * "pixel 32.89031692712137 of 0.1", a float precision artifact with no
   * unit, on the one line whose entire job is telling a reviewer whether a
   * budget passed and by how much.
   */
  it('formats a budget as a rounded percentage against its threshold, not a raw float', async () => {
    const calls = stubServer({
      item: summary({
        budgets: [{ plane: 'pixel', threshold: 0.1, observed: 32.89031692712137, failed: true }],
      }),
    });
    await mount();
    await openAFlow(calls);

    const section = container.querySelector('.item__budgets');
    expect(section?.textContent).toContain('32.89%');
    expect(section?.textContent).toContain('0.1%');
    expect(section?.textContent).not.toContain('32.89031692712137');
  });
});

function checkpointFixture(name: string, verdict: 'ok' | 'changed'): NonNullable<Summary['checkpoints']>[number] {
  return {
    name,
    verdict,
    diffPct: verdict === 'ok' ? 0 : 4.2,
    diffPctFine: 0,
    numDiff: verdict === 'ok' ? 0 : 40,
    images: { a: `${name}.png`, b: `${name}.png`, diff: verdict === 'ok' ? '' : `${name}-diff.png` },
    at: '0001-01-01T00:00:00Z',
  };
}

/**
 * A flow with many checkpoints renders one full ShotCompare grid PER
 * checkpoint — for a project whose flows carry a dozen+ screens, most of
 * which passed, that is a long scroll to reach wire/hops/budgets on every
 * single review. Passing checkpoints collapse behind a disclosure so the
 * screens that actually need a look are what's on screen by default.
 */
describe('collapsible passing checkpoints', () => {
  it('shows changed checkpoints and collapses passing ones behind a disclosure', async () => {
    const calls = stubServer({
      item: summary({
        checkpoints: [
          checkpointFixture('cart', 'changed'),
          checkpointFixture('login', 'ok'),
          checkpointFixture('search', 'ok'),
          checkpointFixture('footer', 'ok'),
        ],
      }),
    });
    await mount();
    await openAFlow(calls);

    expect(container.textContent).toContain('cart');
    expect(container.querySelector('.shot-compare')).not.toBeNull();
    // The three passing checkpoints are not rendered as full compare grids
    // until expanded.
    expect(container.querySelectorAll('.shot-compare')).toHaveLength(1);

    const disclosure = Array.from(container.querySelectorAll('button')).find((b) =>
      b.textContent?.includes('3 unchanged'),
    );
    expect(disclosure).toBeTruthy();

    await act(async () => disclosure!.click());
    expect(container.querySelectorAll('.shot-compare')).toHaveLength(4);
  });

  it('shows no disclosure when every checkpoint changed', async () => {
    const calls = stubServer({
      item: summary({ checkpoints: [checkpointFixture('cart', 'changed')] }),
    });
    await mount();
    await openAFlow(calls);

    const disclosure = Array.from(container.querySelectorAll('button')).find((b) =>
      b.textContent?.includes('unchanged'),
    );
    expect(disclosure).toBeUndefined();
  });
});

/**
 * Evidence (video/report) is fetched independently of the summary — it
 * attaches to a run AFTER `retrace run`/sync finishes, so it is never part
 * of Summary and must not blank the pixel/wire/hop planes if its own fetch
 * fails or comes back empty.
 */
describe('evidence (video + report)', () => {
  it('renders nothing when the candidate run has no evidence', async () => {
    const calls = stubServer({ evidence: { videos: [], hasReport: false } });
    await mount();
    await openAFlow(calls);
    expect(container.querySelector('.item__evidence')).toBeNull();
  });

  it('plays a video and links to the full report', async () => {
    const calls = stubServer({
      evidence: { videos: ['card-views.webm'], hasReport: true },
    });
    await mount();
    await openAFlow(calls);

    const video = container.querySelector('.item__video') as HTMLVideoElement | null;
    expect(video).not.toBeNull();
    expect(video?.getAttribute('src')).toBe('/api/videos/web/search/card-views.webm');

    const link = container.querySelector('.item__report-link') as HTMLAnchorElement | null;
    expect(link).not.toBeNull();
    expect(link?.getAttribute('href')).toBe('/api/report/web/search/');
    expect(link?.getAttribute('target')).toBe('_blank');
  });
});
