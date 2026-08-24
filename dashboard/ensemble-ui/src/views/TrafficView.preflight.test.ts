import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import TrafficView from './TrafficView';
import { api } from '../api/client';
import * as sse from '../api/sse';
import type { Hop, Topology } from '../api/types';

// CORS preflight hops are noisy (every cross-origin request generating one) but valuable when
// diagnosing a misconfigured gateway CORS policy — so they're suppressed by default and revealed
// via a toggle, the same pattern as the "show gateways" toggle.

const PREFLIGHT: Hop = {
  schema: 'hop.v1',
  seq: 1,
  to: 'public',
  method: 'OPTIONS',
  path: '/widgets/1',
  status: 204,
  preflight: true,
  t: { start: '2026-08-21T00:00:00.000Z' },
};
const REAL: Hop = {
  schema: 'hop.v1',
  seq: 2,
  to: 'public',
  method: 'GET',
  path: '/widgets/1',
  status: 200,
  t: { start: '2026-08-21T00:00:00.010Z' },
};

const EMPTY_TOPOLOGY: Topology = { nodes: [], edges: [] };

describe('TrafficView: CORS preflight visibility', () => {
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

  it('suppresses CORS preflight hops by default', async () => {
    vi.spyOn(api, 'traffic').mockResolvedValue([PREFLIGHT, REAL]);
    vi.spyOn(api, 'topology').mockResolvedValue(EMPTY_TOPOLOGY);
    vi.spyOn(sse, 'subscribeHops').mockReturnValue(() => {});

    root = createRoot(container);
    await act(async () => {
      root.render(createElement(TrafficView));
    });

    expect(container.querySelectorAll('tbody tr')).toHaveLength(1);
    expect(container.innerHTML).not.toContain('ds-badge--blue');
  });

  it('the "show CORS preflight" toggle reveals suppressed preflight hops', async () => {
    vi.spyOn(api, 'traffic').mockResolvedValue([PREFLIGHT, REAL]);
    vi.spyOn(api, 'topology').mockResolvedValue(EMPTY_TOPOLOGY);
    vi.spyOn(sse, 'subscribeHops').mockReturnValue(() => {});

    root = createRoot(container);
    await act(async () => {
      root.render(createElement(TrafficView));
    });
    expect(container.querySelectorAll('tbody tr')).toHaveLength(1);

    const toggle = Array.from(container.querySelectorAll('button')).find(
      (b) => b.textContent === 'show CORS preflight',
    ) as HTMLButtonElement;
    expect(toggle).toBeDefined();
    await act(async () => {
      toggle.click();
    });

    expect(container.querySelectorAll('tbody tr')).toHaveLength(2);
    expect(container.innerHTML).toContain('ds-badge--blue');
  });
});
