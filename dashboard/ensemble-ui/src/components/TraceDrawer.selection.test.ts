import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import TraceDrawer from './TraceDrawer';
import { api, type TraceResponse } from '../api/client';
import type { Hop } from '../api/types';

// Clicking a hop in the trace drawer's timing panel selects it, but nothing ever rendered the
// payload (headers/body/timing) for that selection — the drawer reused TopologyGraph +
// HopTimingPanel only. This is the direct regression test for wiring HopDetail into the drawer
// so selecting a hop shows its request/response detail alongside the waterfall.

function hop(seq: number, to: string): Hop {
  return {
    schema: 'ensemble/1',
    seq,
    from: 'client',
    to,
    method: 'GET',
    path: '/widgets',
    status: 200,
    t: { start: '2026-01-01T00:00:00.000Z', doneMs: 5 },
    req: { headers: { 'x-request-id': 'abc123' } },
    resp: { headers: { 'content-type': 'application/json' }, body: '{"ok":true}' },
  };
}

describe('TraceDrawer: hop detail panel', () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    container = document.createElement('div');
    document.body.appendChild(container);
    const response: TraceResponse = { hops: [hop(1, 'backend')], logical: [] };
    vi.spyOn(api, 'trace').mockResolvedValue(response);
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    vi.restoreAllMocks();
  });

  it('renders the selected hop\'s request/response detail after clicking it', async () => {
    root = createRoot(container);
    await act(async () => {
      root.render(createElement(TraceDrawer, { traceId: 'trace-1', onClose: () => {} }));
    });

    expect(container.querySelector('.hop-detail')).toBeFalsy();

    const hopRow = container.querySelector('.topo-hop-row');
    expect(hopRow, 'expected a clickable hop row in the timing panel').toBeTruthy();
    await act(async () => {
      (hopRow as HTMLButtonElement).click();
    });

    const detail = container.querySelector('.hop-detail');
    expect(detail, 'expected a hop detail panel to render once a hop is selected').toBeTruthy();
    expect(detail!.textContent).toContain('x-request-id');

    const responseTab = Array.from(detail!.querySelectorAll('[role="tab"]')).find(
      (b) => b.textContent === 'response',
    ) as HTMLButtonElement;
    expect(responseTab, 'expected a response tab in the hop detail panel').toBeTruthy();
    await act(async () => {
      responseTab.click();
    });
    expect(detail!.textContent).toContain('content-type');
  });
});
