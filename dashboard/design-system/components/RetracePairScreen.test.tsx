import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { createRetraceClient } from '../retraceClient';
import type { Summary } from '../retraceTypes';
import RetracePairScreen from './RetracePairScreen';

function fakeResponse(body: unknown) {
  return { ok: true, status: 200, statusText: 'OK', text: () => Promise.resolve(JSON.stringify(body)) };
}

function pairSummary(): Summary {
  return {
    schema: 'retrace-diff/1',
    app: 'web',
    flow: 'wallet-home',
    verdict: 'changed',
    a: { runId: 'reference', kind: 'bundle', dir: '/a', manifest: { app: 'web' } as never },
    b: { runId: '20260904T120000Z-abc1234', kind: 'run', dir: '/b', manifest: { app: 'mobile' } as never },
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
  };
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

describe('RetracePairScreen', () => {
  it('fetches the persisted pairing and labels the sides by app, not reference/latest', async () => {
    const calls: string[] = [];
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL) => {
        const url = typeof input === 'string' ? input : String(input);
        calls.push(url);
        // RetraceItemScreen always fetches evidence for the app/flow it's
        // given, in addition to the pairing itself — route by URL rather
        // than answering every request with the same pairSummary body, or
        // EvidenceSection's `data.videos.length` throws on a body with no
        // `videos` field.
        const body = url.includes('/evidence/') ? { videos: [], hasReport: false } : { summary: pairSummary() };
        return Promise.resolve(fakeResponse(body));
      }),
    );
    const client = createRetraceClient('/api');

    await act(async () => {
      root.render(
        <RetracePairScreen
          client={client}
          appB="mobile"
          flowB="wallet-home"
          runB="20260904T120000Z-abc1234"
          pairId="web__reference"
        />,
      );
    });

    expect(calls).toContain('/api/pairs/mobile/wallet-home/20260904T120000Z-abc1234/web__reference');
    expect(container.textContent).toContain('web → mobile');
  });
});
