import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import TrafficView from './TrafficView';
import { api } from '../api/client';
import * as sse from '../api/sse';
import type { Hop, Topology } from '../api/types';

// The session filter is a dropdown, not tabs, and only ever renders once
// there are >1 distinct hop.session ids to choose between — with 0 or 1
// sessions in play, "filter by session" is a no-op, so the control stays
// out of the way entirely.

const AMBIENT: Hop = {
  schema: 'hop.v1',
  seq: 1,
  to: 'public',
  method: 'GET',
  path: '/widgets',
  status: 200,
  t: { start: '2026-08-21T00:00:00.000Z' },
};
const SESSION_A: Hop = {
  schema: 'hop.v1',
  seq: 2,
  to: 'public',
  method: 'GET',
  path: '/widgets/1',
  status: 200,
  session: 'aaaaaaaa-1111',
  t: { start: '2026-08-21T00:00:00.010Z' },
};
const SESSION_B: Hop = {
  schema: 'hop.v1',
  seq: 3,
  to: 'public',
  method: 'GET',
  path: '/widgets/2',
  status: 200,
  session: 'bbbbbbbb-2222',
  t: { start: '2026-08-21T00:00:00.020Z' },
};

const EMPTY_TOPOLOGY: Topology = { nodes: [], edges: [] };

describe('TrafficView: session filter', () => {
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

  it('hides the session dropdown when at most one session is present', async () => {
    vi.spyOn(api, 'traffic').mockResolvedValue([AMBIENT, SESSION_A]);
    vi.spyOn(api, 'topology').mockResolvedValue(EMPTY_TOPOLOGY);
    vi.spyOn(sse, 'subscribeHops').mockReturnValue(() => {});

    root = createRoot(container);
    await act(async () => {
      root.render(createElement(TrafficView));
    });

    expect(container.querySelector('.traffic-view__session-select')).toBeNull();
    expect(container.querySelectorAll('tbody tr')).toHaveLength(2);
  });

  it('shows the dropdown once a second distinct session appears, and filters by it', async () => {
    vi.spyOn(api, 'traffic').mockResolvedValue([AMBIENT, SESSION_A, SESSION_B]);
    vi.spyOn(api, 'topology').mockResolvedValue(EMPTY_TOPOLOGY);
    vi.spyOn(sse, 'subscribeHops').mockReturnValue(() => {});

    root = createRoot(container);
    await act(async () => {
      root.render(createElement(TrafficView));
    });

    const select = container.querySelector('.traffic-view__session-select') as HTMLSelectElement;
    expect(select).not.toBeNull();
    expect(Array.from(select.options).map((o) => o.value)).toEqual([
      'all',
      'ambient',
      'aaaaaaaa-1111',
      'bbbbbbbb-2222',
    ]);
    expect(container.querySelectorAll('tbody tr')).toHaveLength(3);

    await act(async () => {
      select.value = 'aaaaaaaa-1111';
      select.dispatchEvent(new Event('change', { bubbles: true }));
    });
    expect(container.querySelectorAll('tbody tr')).toHaveLength(1);

    await act(async () => {
      select.value = 'ambient';
      select.dispatchEvent(new Event('change', { bubbles: true }));
    });
    expect(container.querySelectorAll('tbody tr')).toHaveLength(1);
  });
});
