import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import ServicesView from './ServicesView';
import { api } from '../api/client';
import type { ServiceState, Topology } from '../api/types';

// Final review F2 & F5: useAsync clears BOTH `data` and `error` to null the instant a new
// poll starts, so naively rendering `error` straight off the hook flashed the "offline"
// banner back to the stale-but-good table for the whole duration of every in-flight poll
// while the backend was down — for a poll that keeps failing every ~5s, effectively the
// entire outage. Pre-migration `setError(null)` ran only on the success path. Also: the
// fallback message for a non-ApiError failure (a raw fetch/network error) must be the
// friendly "failed to reach the ensemble API", not whatever `Error#message` happens to say.

const TOPOLOGY: Topology = { nodes: [], edges: [] };
const POLL_MS = 5000;

function statusOf(status: string): ServiceState[] {
  return [{ name: 'svc', status, placement: 'native' }];
}

describe('ServicesView: the offline banner stays up across an in-flight poll', () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    vi.useFakeTimers();
    container = document.createElement('div');
    document.body.appendChild(container);
    vi.spyOn(api, 'topology').mockResolvedValue(TOPOLOGY);
    vi.spyOn(api, 'wiringWarnings').mockResolvedValue([]);
    vi.spyOn(api, 'gatewayStatus').mockResolvedValue([]);
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    vi.restoreAllMocks();
    vi.useRealTimers();
  });

  it('keeps showing "offline" (with the friendly fallback message) while the next poll is in flight', async () => {
    let call = 0;
    let resolveThird!: (s: ServiceState[]) => void;
    vi.spyOn(api, 'status').mockImplementation(() => {
      call += 1;
      if (call === 1) return Promise.resolve(statusOf('healthy'));
      // A raw network failure (not an ApiError) — messageOf's fallback must be used, not
      // this object's own message.
      if (call === 2) return Promise.reject(new TypeError('Failed to fetch'));
      if (call === 3) {
        return new Promise<ServiceState[]>((r) => {
          resolveThird = r;
        });
      }
      throw new Error(`unexpected status() call #${call}`);
    });

    root = createRoot(container);
    await act(async () => {
      root.render(createElement(ServicesView));
    });
    expect(container.querySelector('.services-table__row')).toBeTruthy();

    // Poll #2 fails — the offline banner must appear with the friendly fallback message,
    // not the raw "Failed to fetch".
    await act(async () => {
      await vi.advanceTimersByTimeAsync(POLL_MS);
    });
    expect(container.querySelector('.services-view--error')).toBeTruthy();
    expect(container.textContent).toContain('failed to reach the ensemble API');
    expect(container.textContent).not.toContain('Failed to fetch');

    // Poll #3 starts (useAsync clears data+error to null synchronously) and is held
    // in flight — the banner must still be up, not replaced by the stale-but-good table.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(POLL_MS);
    });
    expect(container.querySelector('.services-view--error')).toBeTruthy();
    expect(container.querySelector('.services-table__row')).toBeNull();

    // Once poll #3 actually succeeds, the table comes back and the banner clears.
    await act(async () => {
      resolveThird(statusOf('healthy'));
    });
    expect(container.querySelector('.services-view--error')).toBeNull();
    expect(container.querySelector('.services-table__row')).toBeTruthy();
  });

  it('final review F7: Stop stays busy until the refreshed row is actually on screen', async () => {
    let call = 0;
    let resolveRefresh!: (s: ServiceState[]) => void;
    vi.spyOn(api, 'status').mockImplementation(() => {
      call += 1;
      if (call === 1) return Promise.resolve(statusOf('healthy'));
      if (call === 2) {
        return new Promise<ServiceState[]>((r) => {
          resolveRefresh = r;
        });
      }
      throw new Error(`unexpected status() call #${call}`);
    });
    vi.spyOn(api, 'stop').mockResolvedValue(statusOf('healthy')[0]);

    root = createRoot(container);
    await act(async () => {
      root.render(createElement(ServicesView));
    });

    const stopButton = Array.from(container.querySelectorAll('button')).find((b) => b.textContent === 'Stop');
    expect(stopButton).toBeTruthy();
    await act(async () => {
      stopButton!.click();
    });

    // api.stop() has resolved and refresh()'s own status() call (#2) is now pending — the
    // row must still read "healthy" (not yet "stopped") and Stop's busy spinner must still
    // be up, not have cleared before the refreshed row is actually on screen.
    const row = container.querySelector('.services-table__row');
    expect(row?.textContent).toContain('healthy');
    expect(row?.querySelector('.ds-spinner'), 'Stop must still be busy while refresh() is in flight').toBeTruthy();

    await act(async () => {
      resolveRefresh(statusOf('stopped'));
    });
    expect(container.querySelector('.services-table__row')?.textContent).toContain('stopped');
  });
});
