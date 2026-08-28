import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import RetraceView from './RetraceView';
import { api, ApiError } from '../api/client';
import type { RetraceItem, RetraceQueueResponse, RetraceSummary } from '../api/types';

async function flush(turns = 8) {
  for (let i = 0; i < turns; i++) {
    // eslint-disable-next-line no-await-in-loop
    await act(async () => {
      await Promise.resolve();
    });
  }
}

function item(overrides: Partial<RetraceItem> = {}): RetraceItem {
  return {
    app: 'web',
    flow: 'checkout',
    verdict: 'changed',
    score: 5,
    runId: '20260821T100000Z-aaaaaaa',
    counts: {
      checkpoints: 1,
      pixelChanged: 1,
      wirePaired: 1,
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
    capture: { a: { status: 'ok', summary: 'ok' }, b: { status: 'ok', summary: 'ok' } },
    gates: [],
    ...overrides,
  };
}

function summaryFor(app: string, flow: string): RetraceSummary {
  const manifest = {
    schema: 'retrace/1',
    app,
    flow,
    runId: '20260821T100000Z-aaaaaaa',
    mode: 'standalone' as const,
    git: { sha: 'deadbee', branch: 'main', dirty: false },
    startedAt: '2026-08-21T10:00:00Z',
    finishedAt: '2026-08-21T10:00:05Z',
    checkpoints: [],
    groups: [],
    capture: { status: 'ok' as const, summary: 'ok' },
    wire: { calls: 1, recorded: true },
    test: { command: '', exitCode: 0, durationMs: 0 },
    env: { go: '', platform: '', retrace: '' },
  };
  return {
    schema: 'retrace-diff/1',
    app,
    flow,
    verdict: 'changed',
    a: { runId: 'ref', kind: 'bundle', dir: '/tmp/ref', manifest },
    b: { runId: 'cand', kind: 'run', dir: '/tmp/cand', manifest },
    quarantined: [],
    checkpoints: [],
    wire: { paired: [], missing: [], extra: [] },
    sections: [],
    hops: { serviceCounts: [], newRoutes: [], goneRoutes: [], hopRequireConfigured: false },
    unexpectedStatuses: [],
    perf: { status: 'unset', measuredMs: 0, budgetMs: 0 },
    conformance: [],
    openApiConfigured: false,
    capture: { a: { status: 'ok', summary: 'ok' }, b: { status: 'ok', summary: 'ok' } },
    counts: item().counts,
    gates: [],
    budgets: [],
    unmeasuredGates: [],
    suppressions: [],
    triage: { label: '', rule: 'no-signal-moved', signals: { pixel: false, wire: false, hop: false, spec: false, capture: false } },
  };
}

describe('RetraceView', () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    container = document.createElement('div');
    document.body.appendChild(container);
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    vi.restoreAllMocks();
  });

  it('loads and renders the queue', async () => {
    const resp: RetraceQueueResponse = { items: [item()], empty: '' };
    vi.spyOn(api, 'retraceQueue').mockResolvedValue(resp);

    root = createRoot(container);
    await act(async () => {
      root.render(createElement(RetraceView));
    });
    await flush();

    expect(container.textContent).toContain('web/checkout');
    expect(container.textContent).toContain('changed');
  });

  it('row click loads and renders inline detail', async () => {
    vi.spyOn(api, 'retraceQueue').mockResolvedValue({ items: [item()], empty: '' });
    vi.spyOn(api, 'retraceItem').mockResolvedValue(summaryFor('web', 'checkout'));
    vi.spyOn(api, 'retraceEvidence').mockResolvedValue({ videos: [], hasReport: false });

    root = createRoot(container);
    await act(async () => {
      root.render(createElement(RetraceView));
    });
    await flush();

    const row = container.querySelector('.retrace-table__row') as HTMLElement | null;
    expect(row).toBeTruthy();
    await act(async () => {
      row!.click();
    });
    await flush();

    expect(api.retraceItem).toHaveBeenCalledWith('web', 'checkout');
    expect(container.querySelector('.retrace-detail')).toBeTruthy();
  });

  it('shows video and a report link when evidence is present', async () => {
    vi.spyOn(api, 'retraceQueue').mockResolvedValue({ items: [item()], empty: '' });
    vi.spyOn(api, 'retraceItem').mockResolvedValue(summaryFor('web', 'checkout'));
    vi.spyOn(api, 'retraceEvidence').mockResolvedValue({ videos: ['ViewPan.webm'], hasReport: true });

    root = createRoot(container);
    await act(async () => {
      root.render(createElement(RetraceView));
    });
    await flush();

    const row = container.querySelector('.retrace-table__row') as HTMLElement | null;
    await act(async () => {
      row!.click();
    });
    await flush();

    expect(api.retraceEvidence).toHaveBeenCalledWith('web', 'checkout');
    const video = container.querySelector('video');
    expect(video).toBeTruthy();
    expect(video!.getAttribute('src')).toBe('/api/retrace/videos/web/checkout/ViewPan.webm');
    const link = Array.from(container.querySelectorAll('a')).find((a) => a.textContent?.includes('test report'));
    expect(link).toBeTruthy();
    expect(link!.getAttribute('href')).toBe('/api/retrace/report/web/checkout/');
  });

  it('renders no evidence section when none is attached', async () => {
    vi.spyOn(api, 'retraceQueue').mockResolvedValue({ items: [item()], empty: '' });
    vi.spyOn(api, 'retraceItem').mockResolvedValue(summaryFor('web', 'checkout'));
    vi.spyOn(api, 'retraceEvidence').mockResolvedValue({ videos: [], hasReport: false });

    root = createRoot(container);
    await act(async () => {
      root.render(createElement(RetraceView));
    });
    await flush();

    const row = container.querySelector('.retrace-table__row') as HTMLElement | null;
    await act(async () => {
      row!.click();
    });
    await flush();

    expect(container.querySelector('video')).toBeNull();
    expect(container.querySelector('.retrace-detail__evidence')).toBeNull();
  });

  it('a sync failure shows inline rather than crashing', async () => {
    vi.spyOn(api, 'retraceQueue').mockResolvedValue({ items: [], empty: 'no-runs' });
    vi.spyOn(api, 'retraceSync').mockRejectedValue(new ApiError(502, 'gh: command not found'));

    root = createRoot(container);
    await act(async () => {
      root.render(createElement(RetraceView));
    });
    await flush();

    const button = Array.from(container.querySelectorAll('button')).find((b) => b.textContent === 'Sync now');
    expect(button).toBeTruthy();
    await act(async () => {
      button!.click();
    });
    await flush();

    expect(container.textContent).toContain('gh: command not found');
  });
});
