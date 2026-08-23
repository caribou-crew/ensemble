import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import TrafficView from './TrafficView';
import { api } from '../api/client';
import * as sse from '../api/sse';
import type { Hop, Topology } from '../api/types';

// Gateway-collapse + trace-drawer: client -> gateway -> target should read as client -> target
// by default, an `expose_in_traffic: true` gateway (or the toggle) should reveal it, and
// clicking a trace id should open the in-page drawer rather than navigating to Topology.

const OUTER: Hop = {
  schema: 'hop.v1',
  seq: 1,
  traceId: 'trace-1',
  spanId: 's1',
  to: 'public',
  path: '/bff/healthz',
  status: 200,
  t: { start: '2026-08-21T00:00:00.000Z' },
};
const INNER: Hop = {
  schema: 'hop.v1',
  seq: 2,
  traceId: 'trace-1',
  spanId: 's2',
  parentSpanId: 's1',
  from: 'public',
  to: 'storefront',
  path: '/healthz',
  status: 200,
  t: { start: '2026-08-21T00:00:00.010Z' },
};

function topologyWith(exposeInTraffic: boolean): Topology {
  return {
    nodes: [
      { name: 'public', category: 'gateway', status: 'static', entry: true, exposeInTraffic },
      { name: 'storefront', category: 'service', status: 'running' },
    ],
    edges: [],
  };
}

describe('TrafficView: gateway collapsing and the trace drawer', () => {
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

  it('collapses client -> gateway -> target to client -> target by default', async () => {
    vi.spyOn(api, 'traffic').mockResolvedValue([OUTER, INNER]);
    vi.spyOn(api, 'topology').mockResolvedValue(topologyWith(false));
    vi.spyOn(sse, 'subscribeHops').mockReturnValue(() => {});

    root = createRoot(container);
    await act(async () => {
      root.render(createElement(TrafficView));
    });

    expect(container.querySelectorAll('tbody tr')).toHaveLength(1);
    expect(container.innerHTML).toContain('client → storefront');
    expect(container.innerHTML).not.toContain('→ public');
  });

  it('leaves a gateway with expose_in_traffic: true visible by default', async () => {
    vi.spyOn(api, 'traffic').mockResolvedValue([OUTER, INNER]);
    vi.spyOn(api, 'topology').mockResolvedValue(topologyWith(true));
    vi.spyOn(sse, 'subscribeHops').mockReturnValue(() => {});

    root = createRoot(container);
    await act(async () => {
      root.render(createElement(TrafficView));
    });

    expect(container.querySelectorAll('tbody tr')).toHaveLength(2);
    expect(container.innerHTML).toContain('client → public');
    expect(container.innerHTML).toContain('public → storefront');
  });

  it('the "show gateways" toggle reveals a collapsed gateway for the session', async () => {
    vi.spyOn(api, 'traffic').mockResolvedValue([OUTER, INNER]);
    vi.spyOn(api, 'topology').mockResolvedValue(topologyWith(false));
    vi.spyOn(sse, 'subscribeHops').mockReturnValue(() => {});

    root = createRoot(container);
    await act(async () => {
      root.render(createElement(TrafficView));
    });
    expect(container.querySelectorAll('tbody tr')).toHaveLength(1);

    const toggle = Array.from(container.querySelectorAll('button')).find(
      (b) => b.textContent === 'show gateways',
    ) as HTMLButtonElement;
    expect(toggle).toBeDefined();
    await act(async () => {
      toggle.click();
    });

    expect(container.querySelectorAll('tbody tr')).toHaveLength(2);
    expect(container.innerHTML).toContain('client → public');
    expect(container.innerHTML).toContain('public → storefront');
  });

  it('opens a right-docked trace drawer in place instead of navigating away, and closes back to the list', async () => {
    vi.spyOn(api, 'traffic').mockResolvedValue([OUTER, INNER]);
    vi.spyOn(api, 'topology').mockResolvedValue(topologyWith(false));
    vi.spyOn(api, 'trace').mockResolvedValue({ hops: [OUTER, INNER], logical: [] });
    vi.spyOn(sse, 'subscribeHops').mockReturnValue(() => {});

    root = createRoot(container);
    await act(async () => {
      root.render(createElement(TrafficView));
    });

    expect(container.querySelector('.trace-drawer')).toBeNull();

    const traceLink = container.querySelector('.hop-table__trace-link') as HTMLButtonElement;
    expect(traceLink).not.toBeNull();
    await act(async () => {
      traceLink.click();
    });

    expect(container.querySelector('.trace-drawer')).not.toBeNull();
    expect(container.innerHTML).toContain('trace trace-1');
    // Still on the Traffic view underneath — the drawer docks over it, it never navigates away.
    expect(container.querySelector('.traffic-view')).not.toBeNull();

    const closeButton = container.querySelector('.trace-drawer__close') as HTMLButtonElement;
    await act(async () => {
      closeButton.click();
    });

    expect(container.querySelector('.trace-drawer')).toBeNull();
  });
});
