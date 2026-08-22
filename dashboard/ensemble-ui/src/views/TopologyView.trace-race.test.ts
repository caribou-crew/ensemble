import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import TopologyView from './TopologyView';
import { api, type TraceResponse } from '../api/client';
import { writeParams } from '../urlState';
import type { Hop, ServiceState, Topology } from '../api/types';

// Regression test for af48831: two different ?trace= ids can resolve out of order (the
// newer id's fetch can start after the older one's is already in flight, but there's no
// guarantee which network round-trip lands first). useTracePoll must render only the
// currently-selected trace's data regardless of resolution order — a stale, later-arriving
// response for an abandoned trace id must never overwrite the newer one.

function hop(seq: number, from: string | undefined, to: string): Hop {
  return {
    schema: 'ensemble/1',
    seq,
    from,
    to,
    method: 'GET',
    path: '/x',
    status: 200,
    t: { start: '2026-01-01T00:00:00.000Z', doneMs: 5 },
  };
}

const EMPTY_TOPOLOGY: Topology = { nodes: [], edges: [] };
const NO_STATUSES: ServiceState[] = [];
const PLACEHOLDER_SERVICE: ServiceState = { name: 'edge', status: 'healthy', placement: 'docker' };

describe('TopologyView: ?trace= race', () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    container = document.createElement('div');
    document.body.appendChild(container);
    vi.spyOn(api, 'topology').mockResolvedValue(EMPTY_TOPOLOGY);
    vi.spyOn(api, 'status').mockResolvedValue(NO_STATUSES);
    vi.spyOn(api, 'traffic').mockResolvedValue([]);
    // TopologyView calls nine distinct api.* methods (useProfiles' own 5s poll runs
    // unconditionally from mount, independently of anything this test clicks) — mock every
    // one of them, not just the ones this test's own interactions reach, so a real socket is
    // never one unmocked call site away (F.18; see testSetup.ts's suite-wide assertion of the
    // same property).
    vi.spyOn(api, 'profiles').mockResolvedValue({ active: [], profiles: [] });
    vi.spyOn(api, 'profileUp').mockResolvedValue({ active: [], profiles: [] });
    vi.spyOn(api, 'profileDown').mockResolvedValue({ active: [], profiles: [] });
    vi.spyOn(api, 'restart').mockResolvedValue(PLACEHOLDER_SERVICE);
    vi.spyOn(api, 'flip').mockResolvedValue(PLACEHOLDER_SERVICE);
    vi.spyOn(api, 'setVariant').mockResolvedValue(PLACEHOLDER_SERVICE);
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    vi.restoreAllMocks();
    window.history.replaceState(null, '', '/');
  });

  it('renders only the newer trace when the OLDER trace resolves after it', async () => {
    let resolveA!: (r: TraceResponse) => void;
    let resolveB!: (r: TraceResponse) => void;
    const traceA = new Promise<TraceResponse>((r) => {
      resolveA = r;
    });
    const traceB = new Promise<TraceResponse>((r) => {
      resolveB = r;
    });

    vi.spyOn(api, 'trace').mockImplementation((id: string) => {
      if (id === 'trace-a') return traceA;
      if (id === 'trace-b') return traceB;
      throw new Error(`unexpected trace id ${id}`);
    });

    window.history.replaceState(null, '', '/?trace=trace-a');

    root = createRoot(container);
    await act(async () => {
      root.render(createElement(TopologyView));
    });

    // Switch to trace-b before trace-a resolves — a real "clicked a different trace before
    // the first one loaded" sequence, driven the same way an external writeParams caller
    // (another view, browser back/forward) would: patch the URL, fire popstate.
    await act(async () => {
      writeParams({ trace: 'trace-b' });
      window.dispatchEvent(new PopStateEvent('popstate'));
    });

    // Resolve the NEWER trace first, then let the STALE older one land after it.
    await act(async () => {
      resolveB({ hops: [hop(1, undefined, 'svc-b')], logical: [] });
    });
    await act(async () => {
      resolveA({ hops: [hop(1, undefined, 'svc-a')], logical: [] });
    });

    expect(container.innerHTML).toContain('svc-b');
    expect(container.innerHTML).not.toContain('svc-a');
    expect(container.querySelector('.topo-view__trace-bar')?.textContent).toContain('trace-b');
  });

  it('renders the newer trace even when it resolves first, unaffected by later stale delivery', async () => {
    // Same race, opposite arrival order — guards against a fix that only special-cased "old
    // resolves after new" instead of unconditionally ignoring any non-current trace id.
    let resolveA!: (r: TraceResponse) => void;
    let resolveB!: (r: TraceResponse) => void;
    const traceA = new Promise<TraceResponse>((r) => {
      resolveA = r;
    });
    const traceB = new Promise<TraceResponse>((r) => {
      resolveB = r;
    });

    vi.spyOn(api, 'trace').mockImplementation((id: string) => {
      if (id === 'trace-a') return traceA;
      if (id === 'trace-b') return traceB;
      throw new Error(`unexpected trace id ${id}`);
    });

    window.history.replaceState(null, '', '/?trace=trace-a');
    root = createRoot(container);
    await act(async () => {
      root.render(createElement(TopologyView));
    });

    await act(async () => {
      writeParams({ trace: 'trace-b' });
      window.dispatchEvent(new PopStateEvent('popstate'));
    });

    await act(async () => {
      resolveB({ hops: [hop(1, undefined, 'svc-b')], logical: [] });
    });

    expect(container.innerHTML).toContain('svc-b');

    await act(async () => {
      resolveA({ hops: [hop(1, undefined, 'svc-a')], logical: [] });
    });

    expect(container.innerHTML).toContain('svc-b');
    expect(container.innerHTML).not.toContain('svc-a');
  });

  it('drops the previous trace immediately on switch, before the new one resolves', async () => {
    // af48831's own fix, isolated from the ordering guard above (which is really the older
    // `cancelled`-closure pattern): without the immediate setHops(null), the OLD trace's
    // graph stays on screen — stale but not wrong-final-state — for the whole gap until the
    // new trace's fetch resolves. That flash is what this asserts against.
    let resolveA!: (r: TraceResponse) => void;
    let resolveB!: (r: TraceResponse) => void;
    const traceA = new Promise<TraceResponse>((r) => {
      resolveA = r;
    });
    const traceB = new Promise<TraceResponse>((r) => {
      resolveB = r;
    });

    vi.spyOn(api, 'trace').mockImplementation((id: string) => {
      if (id === 'trace-a') return traceA;
      if (id === 'trace-b') return traceB;
      throw new Error(`unexpected trace id ${id}`);
    });

    window.history.replaceState(null, '', '/?trace=trace-a');
    root = createRoot(container);
    await act(async () => {
      root.render(createElement(TopologyView));
    });

    await act(async () => {
      resolveA({ hops: [hop(1, undefined, 'svc-a')], logical: [] });
    });
    expect(container.innerHTML).toContain('svc-a');

    await act(async () => {
      writeParams({ trace: 'trace-b' });
      window.dispatchEvent(new PopStateEvent('popstate'));
    });

    // trace-b hasn't resolved yet — the view must already have dropped trace-a's stale
    // content rather than continuing to show it until traceB settles.
    expect(container.innerHTML).not.toContain('svc-a');

    await act(async () => {
      resolveB({ hops: [hop(1, undefined, 'svc-b')], logical: [] });
    });
    expect(container.innerHTML).toContain('svc-b');
  });
});
