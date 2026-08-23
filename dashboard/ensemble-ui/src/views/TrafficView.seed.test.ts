import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import TrafficView from './TrafficView';
import { api } from '../api/client';
import * as sse from '../api/sse';
import type { Hop } from '../api/types';

// Final review F9: TrafficView had no test at all, and the review's mutation M8
// (`useHopRing`'s `}, [initial]);` -> `}, []);`) survived — with an empty deps array the
// seed-and-subscribe effect only ever runs on mount, while `initial` is still `null`
// (useAsync's loading sentinel), so it returns early forever: the seed never lands in
// `hops` and `subscribeHops` is never called, even once the GET actually resolves. The
// whole view goes permanently dead and the suite stayed green. This pins both halves: the
// seeded hop renders, and the SSE subscription opens.

const HOP: Hop = {
  schema: 'hop.v1',
  seq: 1,
  to: 'svc-a',
  path: '/widgets',
  t: { start: '2026-08-21T00:00:00.000Z' },
};

describe('TrafficView: seeds from the initial GET and opens the SSE tail', () => {
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

  it('renders the seeded hop and subscribes to the live tail', async () => {
    vi.spyOn(api, 'traffic').mockResolvedValue([HOP]);
    const unsubscribe = vi.fn();
    const subscribeSpy = vi.spyOn(sse, 'subscribeHops').mockReturnValue(unsubscribe);

    root = createRoot(container);
    await act(async () => {
      root.render(createElement(TrafficView));
    });

    expect(container.querySelector('.traffic-view--loading')).toBeNull();
    expect(container.innerHTML).toContain('svc-a');
    expect(container.innerHTML).toContain('/widgets');
    expect(subscribeSpy, 'the seed must hand off to a live SSE subscription').toHaveBeenCalledTimes(1);
    // Seeded from the last hop's own seq, not hardcoded to 0 — a reconnect must not replay it.
    expect(subscribeSpy.mock.calls[0][0]).toBe(1);

    await act(async () => {
      root.unmount();
    });
    expect(unsubscribe, 'unmounting must close the SSE subscription').toHaveBeenCalledTimes(1);
    root = createRoot(container); // afterEach unmounts again; keep it well-defined
  });

  it('clear empties the table without closing the live SSE subscription', async () => {
    vi.spyOn(api, 'traffic').mockResolvedValue([HOP]);
    let deliver: ((hop: Hop) => void) | undefined;
    const unsubscribe = vi.fn();
    vi.spyOn(sse, 'subscribeHops').mockImplementation((_cursor, onHop) => {
      deliver = onHop;
      return unsubscribe;
    });

    root = createRoot(container);
    await act(async () => {
      root.render(createElement(TrafficView));
    });
    expect(container.innerHTML).toContain('svc-a');

    const clearButton = Array.from(container.querySelectorAll('button')).find(
      (b) => b.textContent === 'clear',
    ) as HTMLButtonElement;
    expect(clearButton).toBeDefined();
    await act(async () => {
      clearButton.click();
    });

    expect(container.innerHTML).not.toContain('svc-a');
    expect(container.innerHTML).toContain('no traffic matches these filters');
    // The subscription stays open — clear is visual-only, not a real reset.
    expect(unsubscribe).not.toHaveBeenCalled();

    // A hop delivered after clear still lands normally.
    await act(async () => {
      deliver?.({ ...HOP, seq: 2, to: 'svc-b' });
    });
    expect(container.innerHTML).toContain('svc-b');
  });
});
