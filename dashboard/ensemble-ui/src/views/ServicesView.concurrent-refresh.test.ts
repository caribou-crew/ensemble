import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import ServicesView from './ServicesView';
import { api } from '../api/client';
import type { ServiceState, Topology } from '../api/types';

// Re-review N1: `refresh()` parks its resolver so callers can `await` the reload actually
// landing (final review F7). Every ServiceRow owns its own `busy` and disables only its OWN
// buttons, so two rows are concurrently actionable — and both `handleAction` calls end in
// `await refresh()`. If refresh keeps only the most-recent resolver, the second call
// overwrites the first WITHOUT calling it: row A's `await refresh()` never settles, its
// `finally { setBusy(null) }` never runs, and — because the refreshed data has flipped the
// row to `stopped`, so it renders `Start` while `busy === 'stop'` matches no spinner
// condition — row A is left disabled with no spinner, no error and no explanation until
// something remounts it. One completed load legitimately satisfies EVERY request waiting on
// fresh data, so all parked resolvers must be drained together.

const TOPOLOGY: Topology = { nodes: [], edges: [] };

function bothHealthy(status = 'healthy'): ServiceState[] {
  return [
    { name: 'svc-a', status, placement: 'native' },
    { name: 'svc-b', status, placement: 'native' },
  ];
}

function rowOf(container: HTMLElement, name: string): HTMLElement {
  const row = Array.from(container.querySelectorAll('.services-table__row')).find(
    (r) => r.querySelector('.services-table__name')?.textContent === name,
  );
  expect(row, `row ${name} must be on screen`).toBeTruthy();
  return row as HTMLElement;
}

function clickIn(row: HTMLElement, label: string) {
  const button = Array.from(row.querySelectorAll('button')).find((b) => b.textContent === label);
  expect(button, `row must offer a "${label}" button`).toBeTruthy();
  button!.click();
}

describe('ServicesView: concurrent row actions', () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    vi.useFakeTimers();
    container = document.createElement('div');
    document.body.appendChild(container);
    vi.spyOn(api, 'topology').mockResolvedValue(TOPOLOGY);
    vi.spyOn(api, 'wiringWarnings').mockResolvedValue([]);
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    vi.restoreAllMocks();
    vi.useRealTimers();
  });

  it('re-enables BOTH rows when two rows are actioned concurrently (N1)', async () => {
    const pending: ((s: ServiceState[]) => void)[] = [];
    let call = 0;
    vi.spyOn(api, 'status').mockImplementation(() => {
      call += 1;
      if (call === 1) return Promise.resolve(bothHealthy());
      return new Promise<ServiceState[]>((r) => {
        pending.push(r);
      });
    });
    vi.spyOn(api, 'stop').mockImplementation((name: string) =>
      Promise.resolve({ name, status: 'stopped', placement: 'native' } as ServiceState),
    );

    root = createRoot(container);
    await act(async () => {
      root.render(createElement(ServicesView));
    });

    // Row A stops. api.stop resolves, refresh() parks resolver #1 and bumps tick; that
    // reload's own status() call is held pending.
    await act(async () => {
      clickIn(rowOf(container, 'svc-a'), 'Stop');
    });
    expect(pending.length, 'row A\'s refresh must have started a reload').toBe(1);

    // Row B stops while row A's refresh is still in flight — legal, because row B's buttons
    // were never disabled. refresh() parks resolver #2.
    await act(async () => {
      clickIn(rowOf(container, 'svc-b'), 'Stop');
    });
    expect(pending.length, 'row B\'s refresh must have started its own reload').toBe(2);

    // Every outstanding load settles. Both `await refresh()` calls must now settle, so both
    // rows' `finally { setBusy(null) }` runs.
    await act(async () => {
      pending.forEach((resolve) => resolve(bothHealthy('stopped')));
    });

    for (const name of ['svc-a', 'svc-b']) {
      const row = rowOf(container, name);
      const disabled = Array.from(row.querySelectorAll('button')).filter((b) => b.disabled);
      expect(
        disabled.length,
        `row ${name}: ${disabled.length} button(s) still disabled after every action settled ` +
          `(row HTML: ${row.innerHTML})`,
      ).toBe(0);
      expect(row.querySelector('.ds-spinner'), `row ${name} must not still be spinning`).toBeNull();
    }
  });
});
