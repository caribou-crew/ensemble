import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import InspectorView from './InspectorView';
import { api } from '../api/client';
import type { ChangeEvent, DatabaseInfo, Table } from '../api/types';

// Re-review N2: F2's fourth site. `useRows` reads `error` straight off `useAsync`, and
// `refreshToken` — bumped by the SSE stream on every change event for the selected table,
// and by the toolbar's own refresh button — is one of its deps. useAsync clears `data` AND
// `error` on any deps change, so a rows query that keeps failing showed its banner only in
// the gaps between re-fetches. On a busy table the banner is mostly absent, and a reader who
// sees it vanish concludes the problem cleared up.
//
// `rows` was already made sticky for the flicker-free-refresh half of this; the error was
// not. Both halves of the identity rule are asserted below, because a sticky error that
// never clears is its own bug: switching tables must drop it.

// happy-dom has no EventSource. This mock captures the change callback so the test can drive
// the stream directly — the same stand-in InspectorView.stale-rows.test.ts uses, with the
// callback kept rather than discarded.
const sse = vi.hoisted(() => ({ emit: null as ((ev: ChangeEvent) => void) | null }));
vi.mock('../api/sse', () => ({
  subscribeChanges: (onChange: (ev: ChangeEvent) => void) => {
    sse.emit = onChange;
    return () => {
      sse.emit = null;
    };
  },
}));

const DATABASES: DatabaseInfo[] = [{ name: 'maindb', type: 'postgres' }];
const TABLES: Table[] = [
  { name: 'orders', columns: [{ name: 'id', type: 'int', nullable: false }] },
  { name: 'users', columns: [{ name: 'id', type: 'int', nullable: false }] },
];

function bannerText(container: HTMLElement): string | null {
  return container.querySelector('.inline-error')?.textContent ?? null;
}

describe('InspectorView: the rows error survives an in-flight re-fetch', () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    container = document.createElement('div');
    document.body.appendChild(container);
    sse.emit = null;
    vi.spyOn(api, 'databases').mockResolvedValue(DATABASES);
    vi.spyOn(api, 'databaseSchema').mockResolvedValue(TABLES);
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    vi.restoreAllMocks();
    window.history.replaceState(null, '', '/');
  });

  it('keeps the banner up across an SSE-driven re-fetch, and drops it on a table switch', async () => {
    let call = 0;
    let resolveSecond!: (rows: Record<string, unknown>[]) => void;
    vi.spyOn(api, 'databaseRows').mockImplementation((_db: string, table: string) => {
      if (table === 'users') return Promise.resolve([{ id: 9 }]);
      call += 1;
      // A raw network failure, not an ApiError — messageOf's fallback must be what shows.
      if (call === 1) return Promise.reject(new TypeError('Failed to fetch'));
      if (call === 2) {
        return new Promise<Record<string, unknown>[]>((r) => {
          resolveSecond = r;
        });
      }
      throw new Error(`unexpected databaseRows call #${call} for ${table}`);
    });

    root = createRoot(container);
    await act(async () => {
      root.render(createElement(InspectorView));
    });

    // The initial rows load for `orders` failed: banner up, with the friendly fallback.
    expect(bannerText(container)).toContain('failed to load rows for orders');
    expect(bannerText(container)).not.toContain('Failed to fetch');

    // One change event on the selected table bumps refreshToken, which clears useAsync's
    // error. The banner must NOT blink away while that re-fetch is in flight.
    expect(sse.emit, 'InspectorView must have subscribed to the change stream').toBeTruthy();
    await act(async () => {
      sse.emit!({ db: 'maindb', table: 'orders' } as ChangeEvent);
    });
    const duringRefetch = bannerText(container);
    expect(
      duringRefetch,
      'the rows error must survive the in-flight SSE-driven re-fetch (F2/N2), not flash away',
    ).toBeTruthy();
    expect(duringRefetch).toContain('failed to load rows for orders');

    // Once the re-fetch actually succeeds, it clears — a successful load, not a started one.
    await act(async () => {
      resolveSecond([{ id: 1 }]);
    });
    expect(bannerText(container), 'a successful load must clear the banner').toBeNull();
  });

  it('drops a stale rows error the moment the selected table changes', async () => {
    // `users` is held PENDING on purpose. If it resolved, the success path would clear the
    // banner on its own and this test would pass with or without the identity-keyed clear —
    // which is exactly what it must not do. The window that matters is the one where the new
    // table is selected and its rows have not arrived yet.
    let resolveUsers!: (rows: Record<string, unknown>[]) => void;
    const usersRows = new Promise<Record<string, unknown>[]>((r) => {
      resolveUsers = r;
    });
    vi.spyOn(api, 'databaseRows').mockImplementation((_db: string, table: string) => {
      if (table === 'orders') return Promise.reject(new TypeError('Failed to fetch'));
      return usersRows;
    });

    root = createRoot(container);
    await act(async () => {
      root.render(createElement(InspectorView));
    });
    expect(bannerText(container)).toContain('failed to load rows for orders');

    const usersTab = Array.from(
      container.querySelectorAll<HTMLElement>('.inspector-view__table-btn'),
    ).find((el) => el.textContent?.includes('users'));
    expect(usersTab, 'expected a "users" table button in the sidebar').toBeTruthy();
    await act(async () => {
      usersTab!.click();
    });

    // Sticky must not mean permanent: this error belongs to a table that is no longer shown,
    // and `users` is still loading, so nothing else would clear it.
    expect(
      bannerText(container),
      "orders' error must not survive onto the users table while its own rows load",
    ).toBeNull();

    await act(async () => {
      resolveUsers([{ id: 9 }]);
    });
    expect(bannerText(container)).toBeNull();
  });
});
