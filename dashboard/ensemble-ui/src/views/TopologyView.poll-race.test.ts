import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import TopologyView from './TopologyView';
import { api } from '../api/client';
import type { ServiceState, Topology } from '../api/types';

// Regression test for final-review-phase-3.md's I3: useTopologyPoll.refresh is the third
// instance of this phase's async-race class. Its `cancelled` flag (checked only inside
// tick(), before refresh() is even called) gates STARTING a poll, never APPLYING one — so
// an interval-triggered refresh that began before a user action's out-of-band refresh() can
// still overwrite the newer call's result if it happens to resolve after it. The fix is a
// generation guard: only the LATEST-STARTED refresh's result may ever be applied, regardless
// of resolution order.

const TOPOLOGY: Topology = {
  nodes: [{ name: 'edge', category: 'service', status: 'starting' }],
  edges: [],
};

const POLL_MS = 5000;

function statusOf(status: string): ServiceState[] {
  return [{ name: 'edge', status, placement: 'docker' }];
}

describe('TopologyView: useTopologyPoll.refresh race', () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    vi.useFakeTimers();
    container = document.createElement('div');
    document.body.appendChild(container);
    vi.spyOn(api, 'topology').mockResolvedValue(TOPOLOGY);
    vi.spyOn(api, 'traffic').mockResolvedValue([]);
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    vi.restoreAllMocks();
    vi.useRealTimers();
    window.history.replaceState(null, '', '/');
  });

  it('applies only the LATEST-started refresh, even when an older one resolves after it', async () => {
    let resolveInterval!: (s: ServiceState[]) => void;
    let resolveRestart!: (s: ServiceState[]) => void;
    const intervalStatus = new Promise<ServiceState[]>((r) => {
      resolveInterval = r;
    });
    const restartStatus = new Promise<ServiceState[]>((r) => {
      resolveRestart = r;
    });

    let call = 0;
    vi.spyOn(api, 'status').mockImplementation(() => {
      call += 1;
      if (call === 1) return Promise.resolve(statusOf('starting'));
      if (call === 2) return intervalStatus;
      if (call === 3) return restartStatus;
      throw new Error(`unexpected status() call #${call}`);
    });
    vi.spyOn(api, 'restart').mockResolvedValue(statusOf('starting')[0]);

    root = createRoot(container);
    // Initial mount poll (call #1) resolves synchronously-ish; flush it.
    await act(async () => {
      root.render(createElement(TopologyView));
    });

    // Select the node so its ServicePanel (and Restart button) is on screen.
    const node = container.querySelector('.topo-node');
    expect(node, 'expected one rendered topology node').toBeTruthy();
    await act(async () => {
      node!.dispatchEvent(new MouseEvent('click', { bubbles: true }));
    });
    expect(container.querySelector('.topo-panel')).toBeTruthy();

    // The 5s poll interval fires (call #2, the OLDER of the two racing calls) and is held
    // pending — this stands in for "the periodic poll that was already in flight".
    await act(async () => {
      await vi.advanceTimersByTimeAsync(POLL_MS);
    });

    // The user clicks Restart before the periodic poll above has resolved — its own
    // `await refresh()` is call #3, the NEWER of the two racing calls.
    const restartButton = Array.from(container.querySelectorAll('button')).find(
      (b) => b.textContent === 'Restart',
    );
    expect(restartButton, 'expected a Restart button once a node is selected').toBeTruthy();
    await act(async () => {
      restartButton!.click();
      // Let api.restart()'s resolved promise's .then/await hop run so refresh() (call #3)
      // actually starts before we resolve either racing status() call.
      await Promise.resolve();
      await Promise.resolve();
    });

    // Resolve the NEWER call (#3) first, with 'healthy' — then let the OLDER, periodic
    // call (#2) resolve afterward with 'starting'. A correct generation guard must keep
    // showing 'healthy': call #2 was superseded the moment call #3 started, regardless of
    // which one's network round-trip actually lands first.
    await act(async () => {
      resolveRestart(statusOf('healthy'));
    });
    await act(async () => {
      resolveInterval(statusOf('starting'));
    });

    const panel = container.querySelector('.topo-panel');
    expect(panel?.textContent).toContain('healthy');
    expect(panel?.textContent).not.toContain('starting');
  });
});
