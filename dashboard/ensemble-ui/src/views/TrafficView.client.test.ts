import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import TrafficView from './TrafficView';
import { api } from '../api/client';
import * as sse from '../api/sse';
import type { Hop, Topology } from '../api/types';

// The two-apps-on-one-stack window this whole feature is for: each client
// has an entry hop and a downstream call, and app-next's downstream call
// carries no client identity of its own — it must still land in app-next's
// pane, or the view hides exactly the routing difference it exists to show.
function hop(seq: number, extra: Partial<Hop>): Hop {
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

const LEGACY_ENTRY = hop(1, { traceId: 't1', client: 'app-legacy', to: 'gateway', path: '/home' });
const LEGACY_INNER = hop(2, { traceId: 't1', to: 'wallet', path: '/v1/wallet' });
const NEXT_ENTRY = hop(3, { traceId: 't2', client: 'app-next', to: 'edge', path: '/home' });
const NEXT_INNER = hop(4, { traceId: 't2', to: 'toolkit-api', path: '/v1/profile', status: 404 });
const ORPHAN = hop(5, { traceId: 't3', to: 'health', path: '/healthz' });

const WINDOW = [LEGACY_ENTRY, LEGACY_INNER, NEXT_ENTRY, NEXT_INNER, ORPHAN];
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

describe('TrafficView: client filter', () => {
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

  it('offers every client in the window plus the unattributed bucket', async () => {
    root = await mount(container, WINDOW);
    const select = container.querySelector('.traffic-view__client-select') as HTMLSelectElement;
    expect(Array.from(select.options).map((o) => o.value)).toEqual(['all', 'app-legacy', 'app-next', '(none)']);
  });

  it('narrows to a client INCLUDING the downstream hop that carries no identity', async () => {
    root = await mount(container, WINDOW);
    const select = container.querySelector('.traffic-view__client-select') as HTMLSelectElement;

    await act(async () => {
      select.value = 'app-next';
      select.dispatchEvent(new Event('change', { bubbles: true }));
    });

    const paths = Array.from(container.querySelectorAll('tbody tr .hop-table__path')).map((el) =>
      (el.textContent ?? '').trim(),
    );
    expect(paths).toEqual(['/home', '/v1/profile']);
  });

  it('selects only hops belonging to no client for the unattributed bucket', async () => {
    root = await mount(container, WINDOW);
    const select = container.querySelector('.traffic-view__client-select') as HTMLSelectElement;

    await act(async () => {
      select.value = '(none)';
      select.dispatchEvent(new Event('change', { bubbles: true }));
    });
    expect(container.querySelectorAll('tbody tr')).toHaveLength(1);
  });

  it('labels the malformed-header bucket as such rather than as an app name', async () => {
    root = await mount(container, [
      hop(1, { traceId: 't1', client: 'app-legacy' }),
      hop(2, { traceId: 't9', client: 'client' }),
    ]);
    const select = container.querySelector('.traffic-view__client-select') as HTMLSelectElement;
    const labels = Array.from(select.options).map((o) => o.textContent);
    expect(labels).toContain('client (malformed header)');
    expect(labels).toContain('app-legacy');
  });

  // Same rule the session dropdown follows: a selection whose hops have
  // aged out must not keep filtering everything away with no way back.
  it('falls back to all clients when the selected one leaves the window', async () => {
    root = await mount(container, WINDOW);
    const select = container.querySelector('.traffic-view__client-select') as HTMLSelectElement;
    await act(async () => {
      select.value = 'app-next';
      select.dispatchEvent(new Event('change', { bubbles: true }));
    });
    expect(new URLSearchParams(window.location.search).get('client')).toBe('app-next');

    await act(async () => {
      root.render(createElement(TrafficView));
    });
    // Re-mount with a window that no longer contains app-next at all.
    act(() => root.unmount());
    container.remove();
    container = document.createElement('div');
    document.body.appendChild(container);
    vi.restoreAllMocks();
    root = await mount(container, [LEGACY_ENTRY, LEGACY_INNER, ORPHAN]);

    expect(new URLSearchParams(window.location.search).get('client')).toBeNull();
    expect(container.querySelectorAll('tbody tr').length).toBeGreaterThan(0);
  });
});

describe('TrafficView: side-by-side', () => {
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

  it('offers no split control until two clients are in the window', async () => {
    root = await mount(container, [LEGACY_ENTRY, LEGACY_INNER]);
    const labels = Array.from(container.querySelectorAll('button')).map((b) => b.textContent);
    expect(labels).not.toContain('split by client');
  });

  it('puts each client in its own column on one shared row axis', async () => {
    window.history.replaceState(null, '', '/?split=1&splitLeft=app-legacy&splitRight=app-next');
    root = await mount(container, WINDOW);

    const rows = Array.from(container.querySelectorAll('.split-panes__row'));
    const shape = rows.map((r) => {
      const cells = Array.from(r.querySelectorAll('td'));
      return cells.map((c) => (c.classList.contains('split-panes__cell--empty') ? null : (c.querySelector('.split-panes__path')?.textContent ?? '').trim()));
    });
    expect(shape).toEqual([
      ['/home', null],
      ['/v1/wallet', null],
      [null, '/home'],
      [null, '/v1/profile'],
    ]);
  });

  it('withholds hops belonging to neither pane and says how many', async () => {
    window.history.replaceState(null, '', '/?split=1&splitLeft=app-legacy&splitRight=app-next');
    root = await mount(container, WINDOW);

    const button = Array.from(container.querySelectorAll('button')).find((b) =>
      (b.textContent ?? '').includes('not in either pane'),
    );
    expect(button?.textContent).toContain('1 not in either pane');
    // The unattributed hop is in neither column.
    const cells = Array.from(container.querySelectorAll('.split-panes__path')).map((e) => e.textContent);
    expect(cells).not.toContain('/healthz');
  });

  it('reveals withheld hops in their own labeled list, never folded into a column', async () => {
    window.history.replaceState(null, '', '/?split=1&splitLeft=app-legacy&splitRight=app-next');
    root = await mount(container, WINDOW);

    const button = Array.from(container.querySelectorAll('button')).find((b) =>
      (b.textContent ?? '').includes('not in either pane'),
    ) as HTMLButtonElement;
    await act(async () => {
      button.dispatchEvent(new MouseEvent('click', { bubbles: true }));
    });

    expect(container.querySelector('.traffic-view__withheld')).not.toBeNull();
    const paneCells = Array.from(container.querySelectorAll('.split-panes__path')).map((e) => e.textContent);
    expect(paneCells).not.toContain('/healthz');
    const withheldPaths = Array.from(
      container.querySelectorAll('.traffic-view__withheld .hop-table__path'),
    ).map((e) => (e.textContent ?? '').trim());
    expect(withheldPaths).toEqual(['/healthz']);
  });

  it('restores the arrangement from the URL', async () => {
    window.history.replaceState(null, '', '/?split=1&splitLeft=app-next&splitRight=app-legacy');
    root = await mount(container, WINDOW);

    const heads = Array.from(container.querySelectorAll('.split-panes__head')).map((h) => h.textContent);
    expect(heads).toEqual(['app-next', 'app-legacy']);
  });

  // One stream, partitioned locally. Two filtered subscriptions could
  // reconnect independently and leave the panes at different points in
  // time, which is the one thing a side-by-side must never do.
  it('opens no additional traffic subscription when entering split mode', async () => {
    const subscribe = vi.spyOn(sse, 'subscribeHops').mockReturnValue(() => {});
    vi.spyOn(api, 'traffic').mockResolvedValue(WINDOW);
    vi.spyOn(api, 'topology').mockResolvedValue(EMPTY_TOPOLOGY);

    root = createRoot(container);
    await act(async () => {
      root.render(createElement(TrafficView));
    });
    const before = subscribe.mock.calls.length;

    const toggle = Array.from(container.querySelectorAll('button')).find(
      (b) => b.textContent === 'split by client',
    ) as HTMLButtonElement;
    await act(async () => {
      toggle.dispatchEvent(new MouseEvent('click', { bubbles: true }));
    });

    expect(container.querySelector('.split-panes')).not.toBeNull();
    expect(subscribe.mock.calls.length).toBe(before);
  });
});
