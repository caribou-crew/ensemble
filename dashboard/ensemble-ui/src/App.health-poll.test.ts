import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import App from './App';
import { api } from './api/client';
import type { ServiceState } from './api/types';

// Final review F9: App.tsx had no test at all, and two of the review's mutations survived
// in its health strip poll (`useHealthPoll`):
//   M6 — dropping the sticky `services` snapshot (`if (data !== null) setServices(data)` ->
//        unconditional) flashes the strip back to "connecting…" every 5s poll, because
//        useAsync clears `data` to null the instant the tick bumps, before the new poll
//        resolves.
//   M7 — deleting the interval effect turns this into a mount-only fetch (the brief's own
//        watch-out 1, named as the way a mechanical rewrite goes wrong): the strip would
//        never update again after the first load.
// `?view=entities` keeps EntityView's own load (api.entities) decoupled from api.status, so
// counting api.status() calls here isolates the health strip's own poll from
// TopologyView's (the default tab, which also calls api.status as part of its own poll).

describe('App: health strip poll', () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    vi.useFakeTimers();
    container = document.createElement('div');
    document.body.appendChild(container);
    window.history.replaceState(null, '', '/?view=entities');
    vi.spyOn(api, 'entities').mockResolvedValue([]);
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    vi.restoreAllMocks();
    vi.useRealTimers();
    window.history.replaceState(null, '', '/');
  });

  function statusOf(status: string): ServiceState[] {
    return [{ name: 'svc', status, placement: 'native' }];
  }

  it('keeps the last good count on screen during an in-flight poll, and keeps polling', async () => {
    let call = 0;
    let resolveSecond!: (s: ServiceState[]) => void;
    vi.spyOn(api, 'status').mockImplementation(() => {
      call += 1;
      if (call === 1) return Promise.resolve(statusOf('healthy'));
      if (call === 2) {
        return new Promise<ServiceState[]>((r) => {
          resolveSecond = r;
        });
      }
      throw new Error(`unexpected status() call #${call}`);
    });

    root = createRoot(container);
    await act(async () => {
      root.render(createElement(App));
    });

    expect(container.querySelector('.health-strip')?.textContent).toContain('1 service');

    // The 5s interval fires (call #2) and is held pending — proves M7 (the interval must
    // still be running) and sets up M6's check (the strip must not drop to "connecting…"
    // while that second call is in flight).
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5000);
    });
    expect(call, 'the poll interval must still fire a second status() call').toBe(2);
    expect(container.querySelector('.health-strip')?.textContent).toContain('1 service');
    expect(container.textContent).not.toContain('connecting…');

    await act(async () => {
      resolveSecond(statusOf('healthy'));
    });
    expect(container.querySelector('.health-strip')?.textContent).toContain('1 service');
  });
});

// Final review F2, App.tsx's half. The sticky-error mirror in `useHealthPoll` was added by
// round 1 but nothing exercised it: the re-review's mutation — collapsing the two-branch
// effect back to `setStaleError(error ? … : null)`, which also clears when the NEXT poll
// merely STARTS — left all 115 tests green. The test above only ever succeeds, so the
// stickiness it was written alongside was never reached. This is that missing half: while
// the backend is down, the offline banner must stay up for the whole duration of every
// in-flight retry, not blink back to the stale-but-good count every 5s.
describe('App: the offline banner is sticky across an in-flight retry', () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    vi.useFakeTimers();
    container = document.createElement('div');
    document.body.appendChild(container);
    window.history.replaceState(null, '', '/?view=entities');
    vi.spyOn(api, 'entities').mockResolvedValue([]);
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
    let resolveThird!: (s: ServiceState[]) => void;
    vi.spyOn(api, 'status').mockImplementation(() => {
      call += 1;
      if (call === 1) return Promise.resolve([{ name: 'svc', status: 'healthy', placement: 'native' }]);
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
      root.render(createElement(App));
    });
    expect(container.querySelector('.health-strip--error')).toBeNull();

    // Poll #2 fails.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5000);
    });
    expect(container.querySelector('.health-strip--error')).toBeTruthy();

    // Poll #3 starts and is held in flight. useAsync has cleared its own `error` — the
    // banner must not go with it.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5000);
    });
    expect(
      container.querySelector('.health-strip--error'),
      'the offline banner must survive the in-flight retry (F2), not blink off every 5s',
    ).toBeTruthy();

    // Only an actual success clears it.
    await act(async () => {
      resolveThird([{ name: 'svc', status: 'healthy', placement: 'native' }]);
    });
    expect(container.querySelector('.health-strip--error')).toBeNull();
  });
});
