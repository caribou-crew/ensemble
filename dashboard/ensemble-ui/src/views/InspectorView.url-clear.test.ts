import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import InspectorView from './InspectorView';
import { api } from '../api/client';
import { readParam } from '../urlState';
import type { DatabaseInfo, Table } from '../api/types';

// Regression test for final-review-phase-3.md's I2 / Parked #3: commit 8d83f8e's fix for a
// stale `?entity=` (EntityView.tsx:413-419) was never mirrored to InspectorView's identical
// `?db=`/`?table=` fallback idiom. A `db=stale` (or `table=stale`) that names nothing falls
// back silently to the first available option, leaving the URL disagreeing with what's on
// screen — this pins the fix the same way EntityView.detail-race.test.ts pins its sibling.

vi.mock('../api/sse', () => ({
  subscribeChanges: () => () => {},
}));

const DATABASES: DatabaseInfo[] = [{ name: 'maindb', type: 'postgres' }];
const TABLES: Table[] = [{ name: 'users', columns: [{ name: 'id', type: 'int', nullable: false }] }];

describe('InspectorView: stale ?db=/?table= clearing', () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    container = document.createElement('div');
    document.body.appendChild(container);
    vi.spyOn(api, 'databaseSchema').mockResolvedValue(TABLES);
    vi.spyOn(api, 'databaseRows').mockResolvedValue([]);
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    vi.restoreAllMocks();
    window.history.replaceState(null, '', '/');
  });

  it('clears ?db= (and its dependent ?table=) when the db names nothing', async () => {
    vi.spyOn(api, 'databases').mockResolvedValue(DATABASES);

    window.history.replaceState(null, '', '/?view=inspector&db=stale&table=users');
    root = createRoot(container);
    await act(async () => {
      root.render(createElement(InspectorView));
    });

    expect(readParam('db'), '?db=stale names nothing and must be cleared').toBeNull();
    expect(readParam('table'), '?table= is only meaningful relative to ?db= and must clear with it').toBeNull();
    // The fallback still renders the first real database/table underneath.
    expect(container.textContent).toContain('maindb');
  });

  it('clears ?table= when it names nothing under a VALID ?db=', async () => {
    vi.spyOn(api, 'databases').mockResolvedValue(DATABASES);

    window.history.replaceState(null, '', '/?view=inspector&db=maindb&table=stale');
    root = createRoot(container);
    await act(async () => {
      root.render(createElement(InspectorView));
    });

    expect(readParam('db'), '?db=maindb is valid and must survive').toBe('maindb');
    expect(readParam('table'), '?table=stale names nothing under maindb and must be cleared').toBeNull();
  });

  it('leaves a VALID ?db=&?table= alone', async () => {
    vi.spyOn(api, 'databases').mockResolvedValue(DATABASES);

    window.history.replaceState(null, '', '/?view=inspector&db=maindb&table=users');
    root = createRoot(container);
    await act(async () => {
      root.render(createElement(InspectorView));
    });

    expect(readParam('db')).toBe('maindb');
    expect(readParam('table')).toBe('users');
  });
});
