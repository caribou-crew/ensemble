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
    // TopologyView calls nine distinct api.* methods (useProfiles' own 5s poll runs
    // unconditionally from mount, independently of anything this test clicks) — mock every
    // one of them, not just the ones this test's own interactions reach, so a real socket is
    // never one unmocked call site away (F.18; see testSetup.ts's suite-wide assertion of the
    // same property).
    vi.spyOn(api, 'profiles').mockResolvedValue({ active: [], profiles: [] });
    vi.spyOn(api, 'profileUp').mockResolvedValue({ active: [], profiles: [] });
    vi.spyOn(api, 'profileDown').mockResolvedValue({ active: [], profiles: [] });
    vi.spyOn(api, 'flip').mockResolvedValue(statusOf('healthy')[0]);
    vi.spyOn(api, 'setVariant').mockResolvedValue(statusOf('healthy')[0]);
    vi.spyOn(api, 'trace').mockRejectedValue(new Error('this test never sets ?trace='));
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

// Final review F2, TopologyView's half — the third site of the sticky-error fix and, like
// App.tsx's, added by round 1 with nothing exercising it: the re-review's mutation
// (collapsing the two-branch effect to one that also clears when the next poll STARTS) left
// the whole suite green. Same property as ServicesView.stale-error.test.ts asserts for its
// own view.
describe('TopologyView: the offline banner is sticky across an in-flight poll', () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    vi.useFakeTimers();
    container = document.createElement('div');
    document.body.appendChild(container);
    vi.spyOn(api, 'traffic').mockResolvedValue([]);
    vi.spyOn(api, 'profiles').mockResolvedValue({ active: [], profiles: [] });
    vi.spyOn(api, 'trace').mockRejectedValue(new Error('this test never sets ?trace='));
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    vi.restoreAllMocks();
    vi.useRealTimers();
    window.history.replaceState(null, '', '/');
  });

  it('keeps "offline" up while the next poll is in flight, and clears it only on success', async () => {
    let call = 0;
    let resolveThird!: (t: Topology) => void;
    vi.spyOn(api, 'status').mockResolvedValue(statusOf('healthy'));
    vi.spyOn(api, 'topology').mockImplementation(() => {
      call += 1;
      if (call === 1) return Promise.resolve(TOPOLOGY);
      if (call === 2) return Promise.reject(new TypeError('Failed to fetch'));
      if (call === 3) {
        return new Promise<Topology>((r) => {
          resolveThird = r;
        });
      }
      throw new Error(`unexpected topology() call #${call}`);
    });

    root = createRoot(container);
    await act(async () => {
      root.render(createElement(TopologyView));
    });
    expect(container.querySelector('.topo-view--error')).toBeNull();

    await act(async () => {
      await vi.advanceTimersByTimeAsync(POLL_MS);
    });
    expect(container.querySelector('.topo-view--error')).toBeTruthy();
    // The fallback, not the raw TypeError message.
    expect(container.textContent).toContain('failed to reach the ensemble API');
    expect(container.textContent).not.toContain('Failed to fetch');

    await act(async () => {
      await vi.advanceTimersByTimeAsync(POLL_MS);
    });
    expect(
      container.querySelector('.topo-view--error'),
      'the offline banner must survive the in-flight poll (F2), not flash back to the graph',
    ).toBeTruthy();
    expect(container.querySelector('.topo-node')).toBeNull();

    await act(async () => {
      resolveThird(TOPOLOGY);
    });
    expect(container.querySelector('.topo-view--error')).toBeNull();
  });
});
