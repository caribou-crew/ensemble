import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import TopologyView from './TopologyView';
import { api } from '../api/client';
import { readParam } from '../urlState';
import type { Topology } from '../api/types';

// The "Grouped"/"Call flow" toggle switches between layoutClustered (category boxes) and
// layoutDepth (top-down by call depth, no boxes) without touching trace mode.

const TOPOLOGY: Topology = {
  nodes: [
    { name: 'edge-gateway', category: 'service', status: 'healthy', entry: true },
    { name: 'orders', category: 'service', status: 'healthy' },
    { name: 'orders-db', category: 'database', status: 'healthy' },
  ],
  edges: [
    { from: 'edge-gateway', to: 'orders' },
    { from: 'orders', to: 'orders-db' },
  ],
};

describe('TopologyView: layout mode toggle', () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    container = document.createElement('div');
    document.body.appendChild(container);
    vi.spyOn(api, 'topology').mockResolvedValue(TOPOLOGY);
    vi.spyOn(api, 'status').mockResolvedValue([]);
    vi.spyOn(api, 'traffic').mockResolvedValue([]);
    vi.spyOn(api, 'profiles').mockResolvedValue({ active: [], profiles: [] });
    vi.spyOn(api, 'trace').mockRejectedValue(new Error('this test never sets ?trace='));
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    vi.restoreAllMocks();
    window.history.replaceState(null, '', '/');
  });

  it('defaults to the grouped (clustered) layout', async () => {
    root = createRoot(container);
    await act(async () => {
      root.render(createElement(TopologyView));
    });
    expect(container.querySelectorAll('.topo-cluster').length).toBeGreaterThan(0);
    expect(readParam('topoLayout')).toBeNull();
  });

  it('switches to the boxless call-flow layout and updates the URL', async () => {
    root = createRoot(container);
    await act(async () => {
      root.render(createElement(TopologyView));
    });

    const flowBtn = Array.from(container.querySelectorAll('.topo-view__layout-btn')).find((b) =>
      b.textContent?.includes('Call flow'),
    ) as HTMLButtonElement;
    expect(flowBtn).toBeTruthy();

    await act(async () => {
      flowBtn.click();
    });

    expect(container.querySelectorAll('.topo-cluster').length).toBe(0);
    expect(readParam('topoLayout')).toBe('flow');
    // Every node still renders — just without cluster boxes.
    expect(container.querySelectorAll('.topo-node').length).toBe(3);
  });

  it('switching back to grouped restores the cluster boxes and clears the URL param', async () => {
    window.history.replaceState(null, '', '/?topoLayout=flow');
    root = createRoot(container);
    await act(async () => {
      root.render(createElement(TopologyView));
    });
    expect(container.querySelectorAll('.topo-cluster').length).toBe(0);

    const groupedBtn = Array.from(container.querySelectorAll('.topo-view__layout-btn')).find((b) =>
      b.textContent?.includes('Grouped'),
    ) as HTMLButtonElement;

    await act(async () => {
      groupedBtn.click();
    });

    expect(container.querySelectorAll('.topo-cluster').length).toBeGreaterThan(0);
    expect(readParam('topoLayout')).toBeNull();
  });
});
