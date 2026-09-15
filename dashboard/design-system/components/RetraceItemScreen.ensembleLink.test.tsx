import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { createRetraceClient } from '../retraceClient';
import type { Manifest, Summary } from '../retraceTypes';
import RetraceItemScreen from './RetraceItemScreen';

function fakeResponse(body: unknown) {
  return { ok: true, status: 200, statusText: 'OK', text: () => Promise.resolve(JSON.stringify(body)) };
}

function summaryWith(manifestB: Partial<Manifest>): Summary {
  return {
    schema: 'retrace-diff/1',
    app: 'web',
    flow: 'wallet-home',
    verdict: 'changed',
    a: { runId: 'reference', kind: 'bundle', dir: '/a', manifest: { app: 'web' } as never },
    b: {
      runId: '20260914T120000Z-abc1234',
      kind: 'run',
      dir: '/b',
      manifest: { app: 'web', checkpoints: [], ...manifestB } as never,
    },
    quarantined: [],
    checkpoints: [],
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
  } as unknown as Summary;
}

async function render(root: Root, summary: Summary) {
  // EvidenceSection fetches on mount regardless; the summary itself is a
  // prop, not a fetch.
  vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(fakeResponse({ videos: [], hasReport: false }))));
  const client = createRetraceClient('/api');
  await act(async () => {
    root.render(
      <RetraceItemScreen
        client={client}
        app="web"
        flow="wallet-home"
        summary={summary}
        selectedField={null}
        onSelectField={() => {}}
      />,
    );
  });
}

let container: HTMLDivElement;
let root: Root;
beforeEach(() => {
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

describe('RetraceItemScreen: link to ensemble traffic', () => {
  it('links a run captured against ensemble to its own hops', async () => {
    await render(
      root,
      summaryWith({ ensemble: { api: 'http://127.0.0.1:4700', session: '20260914T120000Z-abc1234' } }),
    );

    const link = container.querySelector('.item__ensemble-link') as HTMLAnchorElement | null;
    expect(link).not.toBeNull();
    expect(link?.getAttribute('href')).toBe(
      'http://127.0.0.1:4700/?view=traffic&session=20260914T120000Z-abc1234',
    );
  });

  // A standalone run has no control plane to point at. A link to the
  // default address would show a reviewer another run's traffic under this
  // run's heading — worse than no link.
  it('renders nothing for a standalone run', async () => {
    await render(root, summaryWith({}));
    expect(container.querySelector('.item__ensemble-link')).toBeNull();
  });
});
