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
