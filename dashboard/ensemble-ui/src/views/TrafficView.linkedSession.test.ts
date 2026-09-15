import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import TrafficView from './TrafficView';
import { api } from '../api/client';
import * as sse from '../api/sse';
import type { Hop, Topology } from '../api/types';

// A session in the URL is a deep link — retrace's "traffic in ensemble"
// link, or a pasted one. It scopes the view to one run's hops on load.

function hop(seq: number, extra: Partial<Hop> = {}): Hop {
  return {
    schema: 'hop.v1',
    seq,
    to: 'svc',
    method: 'GET',
    path: `/p${seq}`,
    status: 200,
    t: { start: `2026-09-14T10:00:0${seq}.000Z` },
    ...extra,
  } as Hop;
}

const RUN_HOP = hop(1, { session: 'run-7' });
const OTHER_HOP = hop(2, { session: 'run-9' });
const AMBIENT = hop(3);
const EMPTY_TOPOLOGY: Topology = { nodes: [], edges: [] };

async function mount(container: HTMLDivElement, hops: Hop[]): Promise<Root> {
  vi.spyOn(api, 'traffic').mockResolvedValue(hops);
  vi.spyOn(api, 'topology').mockResolvedValue(EMPTY_TOPOLOGY);
  vi.spyOn(sse, 'subscribeHops').mockReturnValue(() => {});
  const root = createRoot(container);
  await act(async () => {
    root.render(createElement(TrafficView));
  });
  return root;
}

describe('TrafficView: a session deep link', () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    container = document.createElement('div');
    document.body.appendChild(container);
    window.history.replaceState(null, '', '/');
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    vi.restoreAllMocks();
    window.history.replaceState(null, '', '/');
  });

  it('opens scoped to the linked run with no further interaction', async () => {
    window.history.replaceState(null, '', '/?session=run-7');
    root = await mount(container, [RUN_HOP, OTHER_HOP, AMBIENT]);

    const paths = Array.from(container.querySelectorAll('tbody tr .hop-table__path')).map((el) =>
      (el.textContent ?? '').trim(),
    );
    expect(paths).toEqual(['/p1']);
  });

  // The failure this guards: a link to one run's traffic that silently
  // shows every run's. A reader who followed the link reads whatever is on
  // screen AS that run.
  it('shows an empty scope for a run with no hops in the window, never the whole stack', async () => {
    window.history.replaceState(null, '', '/?session=run-absent');
    root = await mount(container, [RUN_HOP, OTHER_HOP, AMBIENT]);

    expect(container.querySelectorAll('tbody tr')).toHaveLength(0);
    expect(container.querySelector('.traffic-view__empty')).not.toBeNull();
  });

  // ...but it must be escapable: the dropdown renders even though the
  // linked session is not one of the window's own, so the scope is never a
  // filter with no control.
  it('offers the linked session in the dropdown so the scope can be left', async () => {
    window.history.replaceState(null, '', '/?session=run-absent');
    root = await mount(container, [RUN_HOP, OTHER_HOP, AMBIENT]);

    const select = container.querySelector('.traffic-view__session-select') as HTMLSelectElement;
    expect(select).not.toBeNull();
    expect(Array.from(select.options).map((o) => o.value)).toContain('run-absent');

    await act(async () => {
      select.value = 'all';
      select.dispatchEvent(new Event('change', { bubbles: true }));
    });
    expect(container.querySelectorAll('tbody tr')).toHaveLength(3);
    expect(new URLSearchParams(window.location.search).get('session')).toBeNull();
  });

  it('writes a dropdown-chosen session into the URL so it is shareable too', async () => {
    root = await mount(container, [RUN_HOP, OTHER_HOP, AMBIENT]);

    const select = container.querySelector('.traffic-view__session-select') as HTMLSelectElement;
    await act(async () => {
      select.value = 'run-9';
      select.dispatchEvent(new Event('change', { bubbles: true }));
    });
    expect(new URLSearchParams(window.location.search).get('session')).toBe('run-9');
  });
});
