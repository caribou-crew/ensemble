import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import TrafficView from './TrafficView';
import { api, type TrafficHistoryResponse } from '../api/client';
import * as sse from '../api/sse';
import type { Hop, Topology } from '../api/types';

// "load earlier" pages GET /api/traffic/history backwards and merges the
// result with the live SSE ring by seq — see TrafficView's useHopRing.

function hop(seq: number, path: string): Hop {
  return {
    schema: 'hop.v1',
    seq,
    to: 'catalog',
    method: 'GET',
    path,
    status: 200,
    t: { start: `2026-08-21T00:${String(seq).padStart(2, '0')}:00.000Z` },
  };
}

const EMPTY_TOPOLOGY: Topology = { nodes: [], edges: [] };

describe('TrafficView: load earlier', () => {
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

  async function renderWithInitial(initial: Hop[]) {
    vi.spyOn(api, 'traffic').mockResolvedValue(initial);
    vi.spyOn(api, 'topology').mockResolvedValue(EMPTY_TOPOLOGY);
    vi.spyOn(sse, 'subscribeHops').mockReturnValue(() => {});
    root = createRoot(container);
    await act(async () => {
      root.render(createElement(TrafficView));
    });
  }

  function loadEarlierButton(): HTMLButtonElement {
    const buttons = Array.from(container.querySelectorAll('button')) as HTMLButtonElement[];
    const btn = buttons.find((b) => b.textContent?.includes('load earlier'));
    if (!btn) throw new Error('load earlier button not found (already at beginning of history?)');
    return btn;
  }

  it('pages backward from the oldest loaded seq and prepends the page ascending', async () => {
    await renderWithInitial([hop(10, '/ten'), hop(11, '/eleven')]);

    const historySpy = vi.spyOn(api, 'trafficHistory').mockResolvedValue({
      hops: [hop(9, '/nine'), hop(8, '/eight')], // newest-first, per the endpoint's contract
      corruptLines: 0,
      hasMore: true,
    } satisfies TrafficHistoryResponse);

    await act(async () => {
      loadEarlierButton().click();
    });

    expect(historySpy).toHaveBeenCalledWith(expect.objectContaining({ before: 10, limit: 200 }));

    const rowPaths = Array.from(container.querySelectorAll('tbody tr')).map((r) => r.textContent ?? '');
    expect(rowPaths).toHaveLength(4);
    expect(rowPaths[0]).toContain('/eight');
    expect(rowPaths[1]).toContain('/nine');
    expect(rowPaths[2]).toContain('/ten');
    expect(rowPaths[3]).toContain('/eleven');

    // A second click pages further back from the new oldest seq (8), not
    // from the original 10.
    historySpy.mockResolvedValue({ hops: [hop(7, '/seven')], corruptLines: 0, hasMore: false });
    await act(async () => {
      loadEarlierButton().click();
    });
    expect(historySpy).toHaveBeenLastCalledWith(expect.objectContaining({ before: 8, limit: 200 }));
  });

  it('shows an end-of-history state once hasMore is false and stops issuing further requests', async () => {
    await renderWithInitial([hop(5, '/five')]);
    const historySpy = vi.spyOn(api, 'trafficHistory').mockResolvedValue({
      hops: [hop(4, '/four')],
      corruptLines: 0,
      hasMore: false,
    });

    await act(async () => {
      loadEarlierButton().click();
    });

    expect(container.textContent).toContain('beginning of history');
    expect(() => loadEarlierButton()).toThrow();
    expect(historySpy).toHaveBeenCalledTimes(1);
  });

  it('treats an empty page as end-of-history even if hasMore were somehow true', async () => {
    await renderWithInitial([hop(5, '/five')]);
    vi.spyOn(api, 'trafficHistory').mockResolvedValue({ hops: [], corruptLines: 0, hasMore: false });

    await act(async () => {
      loadEarlierButton().click();
    });

    expect(container.textContent).toContain('beginning of history');
    const rowPaths = Array.from(container.querySelectorAll('tbody tr')).map((r) => r.textContent ?? '');
    expect(rowPaths).toHaveLength(1); // unchanged — nothing to prepend
  });

  it('shows a loading state while the request is in flight, then clears it', async () => {
    await renderWithInitial([hop(5, '/five')]);
    let resolve: (v: TrafficHistoryResponse) => void = () => {};
    vi.spyOn(api, 'trafficHistory').mockReturnValue(
      new Promise<TrafficHistoryResponse>((r) => {
        resolve = r;
      }),
    );

    act(() => {
      loadEarlierButton().click();
    });
    expect(container.textContent).toContain('loading earlier');

    await act(async () => {
      resolve({ hops: [], corruptLines: 0, hasMore: false });
    });
    expect(container.textContent).toContain('beginning of history');
    expect(container.textContent).not.toContain('loading earlier');
  });

  it('surfaces a fetch failure without wedging the button', async () => {
    await renderWithInitial([hop(5, '/five')]);
    const historySpy = vi
      .spyOn(api, 'trafficHistory')
      .mockRejectedValueOnce(new Error('boom'))
      .mockResolvedValueOnce({ hops: [hop(4, '/four')], corruptLines: 0, hasMore: false });

    await act(async () => {
      loadEarlierButton().click();
    });
    // A plain Error (not an ApiError) falls back to messageOf's generic
    // message — see api/client.ts.
    expect(container.textContent).toContain('failed to load earlier traffic');

    // The failure didn't latch noMoreHistory — the button is still there
    // and a retry succeeds.
    await act(async () => {
      loadEarlierButton().click();
    });
    expect(historySpy).toHaveBeenCalledTimes(2);
    expect(container.textContent).toContain('beginning of history');
  });

  it('live SSE hops keep arriving while paged history is loading', async () => {
    let deliver: (h: Hop) => void = () => {};
    vi.spyOn(api, 'traffic').mockResolvedValue([hop(5, '/five')]);
    vi.spyOn(api, 'topology').mockResolvedValue(EMPTY_TOPOLOGY);
    vi.spyOn(sse, 'subscribeHops').mockImplementation((_since, onHop) => {
      deliver = onHop;
      return () => {};
    });
    root = createRoot(container);
    await act(async () => {
      root.render(createElement(TrafficView));
    });

    let resolveHistory: (v: TrafficHistoryResponse) => void = () => {};
    vi.spyOn(api, 'trafficHistory').mockReturnValue(
      new Promise<TrafficHistoryResponse>((r) => {
        resolveHistory = r;
      }),
    );
    act(() => {
      loadEarlierButton().click();
    });

    act(() => {
      deliver(hop(6, '/six'));
    });
    expect(container.textContent).toContain('/six');

    await act(async () => {
      resolveHistory({ hops: [hop(4, '/four')], corruptLines: 0, hasMore: false });
    });

    const rowPaths = Array.from(container.querySelectorAll('tbody tr')).map((r) => r.textContent ?? '');
    expect(rowPaths).toHaveLength(3);
    expect(rowPaths[0]).toContain('/four');
    expect(rowPaths[1]).toContain('/five');
    expect(rowPaths[2]).toContain('/six');
  });
});
