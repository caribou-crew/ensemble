// Behavioral tests for the thin navigation shell — same pattern as
// retrace-ui's App.test.tsx (a fake `fetch`, driven through real clicks),
// since RetraceView now renders the exact same shared components against
// basePath "/api/retrace" instead of "/api". No keyboard dispatch here:
// this dashboard is mouse-only (see the file-level comment on RetraceView).
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import RetraceView from './RetraceView';
import type { CaptureTrust } from '@ensemble/design-system/diffTypes';
import type { Counts, Item, Summary } from '@ensemble/design-system/retraceTypes';

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
  verdict: 'changed',
  score: 2,
  runId: '20260821T101000Z-bbbbbbb',
  counts: zeroCounts,
  capture: { a: okTrust, b: okTrust },
  gates: [],
  ...over,
});

const QUEUE: Item[] = [item('web', 'checkout')];

const summary = (over: Partial<Summary> = {}): Summary => ({
  schema: 'retrace-diff/1',
  app: 'web',
  flow: 'checkout',
  verdict: 'changed',
  a: {
    runId: 'reference',
    kind: 'bundle',
    dir: '/runs/a',
    manifest: {
      schema: 'retrace-run/1',
      app: 'web',
      flow: 'checkout',
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
      flow: 'checkout',
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
  hops: { serviceCounts: [], newRoutes: [], goneRoutes: [], hopRequireConfigured: false } as never,
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
  triage: { label: '', rule: '', signals: { pixel: false, wire: false, hop: false, spec: false, capture: false } },
  ...over,
});

interface Call {
  url: string;
  method: string;
}

function stubServer(opts: {
  queue?: Item[];
  empty?: string;
  item?: Summary;
  runs?: unknown[];
  evidence?: { videos: string[]; hasReport: boolean };
  candidates?: unknown[];
  posts?: Record<string, unknown>;
  instances?: { key: string; label: string }[];
} = {}) {
  const calls: Call[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === 'string' ? input : String(input);
      const method = init?.method ?? 'GET';
      calls.push({ url, method });

      let body: unknown = { ok: true };
      if (url === '/api/retrace/instances') {
        body = { instances: opts.instances ?? [] };
      } else if (url === '/api/retrace/queue' || url.startsWith('/api/retrace/queue?')) {
        body = { items: opts.queue ?? QUEUE, empty: opts.empty ?? '' };
      } else if (url.startsWith('/api/retrace/evidence/')) {
        body = opts.evidence ?? { videos: [], hasReport: false };
      } else if (url.startsWith('/api/retrace/sync/candidates')) {
        body = { candidates: opts.candidates ?? [] };
      } else if (method === 'GET' && /\/runs$/.test(url.split('?')[0])) {
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
    root.render(<RetraceView />);
  });
}

async function flush(turns = 8) {
  for (let i = 0; i < turns; i++) {
    // eslint-disable-next-line no-await-in-loop
    await act(async () => {
      await Promise.resolve();
    });
  }
}

describe('RetraceView', () => {
  it('loads and renders the queue against basePath /api/retrace', async () => {
    const calls = stubServer();
    await mount();
    await flush();

    expect(calls.some((c) => c.url.startsWith('/api/retrace/queue'))).toBe(true);
    expect(container.querySelector('.queue-row__app')?.textContent).toBe('web');
    expect(container.querySelector('.queue-row__flowname')?.textContent).toBe('checkout');
  });

  it('drills queue -> runs -> detail on click, and back up via the breadcrumb', async () => {
    stubServer();
    await mount();
    await flush();

    const row = container.querySelector('.queue-row') as HTMLElement;
    await act(async () => row.dispatchEvent(new MouseEvent('click', { bubbles: true })));
    await flush();

    expect(container.querySelector('.item')).toBeNull();
    expect(container.querySelector('.queue-table')).not.toBeNull();
    expect(container.textContent).toContain('web/checkout');

    const runOpener = container.querySelector('.queue-row') as HTMLElement;
    await act(async () => runOpener.dispatchEvent(new MouseEvent('click', { bubbles: true })));
    await flush();
    expect(container.querySelector('.item')).not.toBeNull();

    // The back button steps up exactly one level (run -> surface), unlike
    // the breadcrumb's "queue" link, which would jump straight to the top.
    const back = container.querySelector('.breadcrumb__back') as HTMLElement;
    await act(async () => back.dispatchEvent(new MouseEvent('click', { bubbles: true })));
    await flush();
    expect(container.querySelector('.item')).toBeNull();
    expect(container.querySelector('.breadcrumb-bar')).not.toBeNull();
    expect(container.querySelector('.queue-table')).not.toBeNull();
  });

  it('shows video and a report link when evidence is present', async () => {
    stubServer({ evidence: { videos: ['ViewPan.webm'], hasReport: true } });
    await mount();
    await flush();

    const row = container.querySelector('.queue-row') as HTMLElement;
    await act(async () => row.dispatchEvent(new MouseEvent('click', { bubbles: true })));
    await flush();
    const runOpener = container.querySelector('.queue-row') as HTMLElement;
    await act(async () => runOpener.dispatchEvent(new MouseEvent('click', { bubbles: true })));
    await flush();

    const video = container.querySelector('video');
    expect(video).toBeTruthy();
    expect(video!.getAttribute('src')).toBe('/api/retrace/videos/web/checkout/ViewPan.webm');
    const link = Array.from(container.querySelectorAll('a')).find((a) => a.textContent?.includes('test report'));
    expect(link!.getAttribute('href')).toBe('/api/retrace/report/web/checkout/');
  });

  it('opens the sync panel with no repo box, since ensemble.yaml already configured it server-side', async () => {
    stubServer({
      queue: [],
      empty: 'no-runs',
      candidates: [
        {
          repo: 'org/repo',
          databaseId: 1,
          workflowName: 'Retrace Web Replay',
          headBranch: 'main',
          actor: 'octocat',
          event: 'push',
          status: 'completed',
          conclusion: 'success',
          createdAt: '2026-08-27T10:00:00Z',
          url: 'https://github.com/org/repo/actions/runs/1',
          hasArtifacts: true,
          localRuns: [],
        },
      ],
    });
    await mount();
    await flush();

    const button = Array.from(container.querySelectorAll('button')).find((b) => b.textContent === 'Browse & sync…');
    await act(async () => button!.click());
    await flush();

    expect(container.querySelector('input[name="repo"]')).toBeFalsy();
    expect(container.textContent).toContain('Retrace Web Replay');
  });

  it('skips the picker and never sends ?instance= when only one instance is configured', async () => {
    const calls = stubServer({ instances: [{ key: 'web', label: 'web' }] });
    await mount();
    await flush();

    expect(container.textContent).not.toContain('Choose a repo');
    expect(container.querySelector('.queue-table')).not.toBeNull();
    expect(calls.some((c) => c.url.includes('instance='))).toBe(false);
  });

  it('shows a picker when multiple instances are configured, and scopes every call to the chosen one', async () => {
    const calls = stubServer({
      instances: [
        { key: 'web', label: 'Web app' },
        { key: 'backend', label: 'Backend' },
      ],
    });
    await mount();
    await flush();

    expect(container.textContent).toContain('Choose a repo');
    expect(container.querySelector('.queue-table')).toBeNull();

    const webButton = Array.from(container.querySelectorAll('button')).find((b) => b.textContent === 'Web app');
    await act(async () => webButton!.click());
    await flush();

    expect(container.querySelector('.queue-table')).not.toBeNull();
    expect(calls.some((c) => c.url.startsWith('/api/retrace/queue?') && c.url.includes('instance=web'))).toBe(true);
  });
});
