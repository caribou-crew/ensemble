import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import InspectorView from './InspectorView';
import { api } from '../api/client';
import type { DatabaseInfo, Table } from '../api/types';

// happy-dom has no EventSource, and this view's SSE subscription is orthogonal to what these
// tests exercise — stand in with a no-op unsubscribe, same as the fix's own flicker-free
// refresh path (which is driven directly through the refresh button below, not the stream).
vi.mock('../api/sse', () => ({
  subscribeChanges: () => () => {},
}));

// Regression test for final-review-phase-3.md's I1: useRows' effect never cleared `rows`
// (or `error`) when db/table/offset changed — only on the `!db || !table` early return. Since
// `cols` is derived synchronously from the just-clicked table, switching tables painted the
// NEW table's headers over the OLD table's still-in-state rows until the new fetch resolved.
//
// The fix must still keep the SSE-driven `refreshToken` re-fetch (same db/table) flicker-free
// — it must NOT clear rows just because `refresh()` was called for the currently-selected
// table, only on an actual db/table/offset identity change. Both behaviors are covered below.

const DATABASES: DatabaseInfo[] = [{ name: 'maindb', type: 'postgres' }];
const TABLES: Table[] = [
  {
    name: 'orders',
    columns: [
      { name: 'id', type: 'int', nullable: false },
      { name: 'total', type: 'int', nullable: false },
    ],
  },
  {
    name: 'users',
    columns: [
      { name: 'id', type: 'int', nullable: false },
      { name: 'email', type: 'text', nullable: true },
    ],
  },
];

describe('InspectorView: stale rows across a table switch', () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    container = document.createElement('div');
    document.body.appendChild(container);
    vi.spyOn(api, 'databases').mockResolvedValue(DATABASES);
    vi.spyOn(api, 'databaseSchema').mockResolvedValue(TABLES);
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    vi.restoreAllMocks();
    window.history.replaceState(null, '', '/');
  });

  it('does not render the previous table\'s rows under the new table\'s headers', async () => {
    let resolveUsers!: (rows: Record<string, unknown>[]) => void;
    const usersRows = new Promise<Record<string, unknown>[]>((r) => {
      resolveUsers = r;
    });

    vi.spyOn(api, 'databaseRows').mockImplementation((_db: string, table: string) => {
      if (table === 'orders') return Promise.resolve([{ id: 1, total: 42 }]);
      if (table === 'users') return usersRows;
      throw new Error(`unexpected table ${table}`);
    });

    window.history.replaceState(null, '', '/?view=inspector&db=maindb&table=orders');
    root = createRoot(container);
    await act(async () => {
      root.render(createElement(InspectorView));
    });

    expect(container.textContent).toContain('42');

    // Click "users" — its rows fetch is held pending.
    const usersButton = Array.from(container.querySelectorAll('button')).find(
      (b) => b.textContent?.includes('users'),
    );
    expect(usersButton, 'expected a "users" table button in the sidebar').toBeTruthy();
    await act(async () => {
      usersButton!.click();
    });

    // The headers already switched (cols are derived synchronously from activeTable). Without
    // I1's fix, `rows` still holds orders' stale (truthy) data, so the loading guard
    // (`rowsLoading && !rows`) never trips and the stale row renders straight through — under
    // the NEW users headers, "42" just lands in a column named "total" that users doesn't
    // have, so it's invisible by content alone; the observable defect is that a table renders
    // at all (with a stale row count) instead of the loading state.
    expect(container.querySelector('.inspector-view__rows-loading'), 'expected a loading spinner while the new table\'s rows are in flight').toBeTruthy();
    expect(container.querySelector('.inspector-table'), 'must not render a stale-data table while the fetch for the new table is in flight').toBeFalsy();

    await act(async () => {
      resolveUsers([{ id: 7, email: 'a@example.com' }]);
    });

    expect(container.textContent).toContain('a@example.com');
    expect(container.textContent).not.toContain('42');
  });

  it('does not clear rows for the flicker-free SSE-driven refresh of the SAME table', async () => {
    let call = 0;
    let resolveRefresh!: (rows: Record<string, unknown>[]) => void;
    vi.spyOn(api, 'databaseRows').mockImplementation(() => {
      call += 1;
      if (call === 1) return Promise.resolve([{ id: 1, total: 42 }]);
      return new Promise<Record<string, unknown>[]>((r) => {
        resolveRefresh = r;
      });
    });

    window.history.replaceState(null, '', '/?view=inspector&db=maindb&table=orders');
    root = createRoot(container);
    await act(async () => {
      root.render(createElement(InspectorView));
    });
    expect(container.textContent).toContain('42');

    const refreshButton = Array.from(container.querySelectorAll('button')).find(
      (b) => b.textContent === 'refresh',
    );
    expect(refreshButton).toBeTruthy();
    await act(async () => {
      refreshButton!.click();
    });

    // Same db/table/offset — the old rows must still be visible while the re-fetch is in
    // flight, not cleared to a loading/empty state.
    expect(container.textContent).toContain('42');

    await act(async () => {
      resolveRefresh([{ id: 1, total: 99 }]);
    });
    expect(container.textContent).toContain('99');
  });
});
