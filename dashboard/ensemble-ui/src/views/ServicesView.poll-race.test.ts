import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import ServicesView from './ServicesView';
import { api } from '../api/client';
import type { ServiceState, Topology } from '../api/types';

// Same regression shape as TopologyView.poll-race.test.ts's I3 fix: useServicesPoll.refresh
// runs both from the 5s interval and out-of-band after a row action (Stop here), so an
// older in-flight poll resolving after a newer out-of-band refresh must not clobber it.

const TOPOLOGY: Topology = {
  nodes: [{ name: 'svc', category: 'service', status: 'healthy' }],
  edges: [],
};

const POLL_MS = 5000;

function statusOf(status: string): ServiceState[] {
  return [{ name: 'svc', status, placement: 'native' }];
}

describe('ServicesView: useServicesPoll.refresh race', () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    vi.useFakeTimers();
    container = document.createElement('div');
    document.body.appendChild(container);
    vi.spyOn(api, 'topology').mockResolvedValue(TOPOLOGY);
    vi.spyOn(api, 'wiringWarnings').mockResolvedValue([]);
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    vi.restoreAllMocks();
    vi.useRealTimers();
  });

  it('applies only the LATEST-started refresh, even when an older one resolves after it', async () => {
    let resolveInterval!: (s: ServiceState[]) => void;
    let resolveStop!: (s: ServiceState[]) => void;
    const intervalStatus = new Promise<ServiceState[]>((r) => {
      resolveInterval = r;
    });
    const stopStatus = new Promise<ServiceState[]>((r) => {
      resolveStop = r;
    });

    let call = 0;
    vi.spyOn(api, 'status').mockImplementation(() => {
      call += 1;
      if (call === 1) return Promise.resolve(statusOf('healthy'));
      if (call === 2) return intervalStatus;
      if (call === 3) return stopStatus;
      throw new Error(`unexpected status() call #${call}`);
    });
    vi.spyOn(api, 'stop').mockResolvedValue(statusOf('healthy')[0]);

    root = createRoot(container);
    await act(async () => {
      root.render(createElement(ServicesView));
    });

    const stopButton = Array.from(container.querySelectorAll('button')).find(
      (b) => b.textContent === 'Stop',
    );
    expect(stopButton, 'expected a Stop button for the running svc row').toBeTruthy();

    // The 5s poll interval fires (call #2, the OLDER racing call) and is held pending.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(POLL_MS);
    });

    // The user clicks Stop before the periodic poll above resolves — api.stop() resolves
    // immediately, then its own `await refresh()` is call #3, the NEWER racing call.
    await act(async () => {
      stopButton!.click();
      await Promise.resolve();
      await Promise.resolve();
    });

    // Resolve the NEWER call (#3) first, with 'stopped' — then the OLDER, periodic call
    // (#2) afterward with 'healthy'. A correct generation guard keeps showing 'stopped'.
    await act(async () => {
      resolveStop(statusOf('stopped'));
    });
    await act(async () => {
      resolveInterval(statusOf('healthy'));
    });

    const row = container.querySelector('.services-table__row');
    expect(row?.textContent).toContain('stopped');
    expect(row?.textContent).not.toContain('healthy');
  });
});
