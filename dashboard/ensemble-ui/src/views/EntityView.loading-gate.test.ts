import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import EntityView from './EntityView';
import { api } from '../api/client';
import type { EntityInfo } from '../api/types';

// Regression test for final review F3: `edit`/`delete` used to gate on `data !== undefined`,
// but useAsync's in-flight sentinel is `null` (not `undefined`), which passes that check —
// so both buttons rendered, and `delete` actually fired, during the initial GET and during
// every post-save refetch. Fixed via `hasRecord = !loading && data !== undefined`.

const ENTITIES: EntityInfo[] = [{ name: 'users', id: 'id' }];

describe('EntityView: edit/delete stay hidden until the load actually settles', () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    container = document.createElement('div');
    document.body.appendChild(container);
    vi.spyOn(api, 'entities').mockResolvedValue(ENTITIES);
    vi.spyOn(api, 'entityList').mockResolvedValue([]);
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    vi.restoreAllMocks();
    window.history.replaceState(null, '', '/');
  });

  it('hides edit/delete while the initial GET is in flight, and shows them once it settles', async () => {
    let resolveGet!: (v: unknown) => void;
    vi.spyOn(api, 'entityGet').mockImplementation(
      () =>
        new Promise((r) => {
          resolveGet = r;
        }),
    );
    vi.spyOn(api, 'entityDelete').mockResolvedValue(undefined);

    window.history.replaceState(null, '', '/?entity=users&id=1');
    root = createRoot(container);
    await act(async () => {
      root.render(createElement(EntityView));
    });

    const buttonsWhileLoading = Array.from(container.querySelectorAll('button')).map((b) => b.textContent);
    expect(buttonsWhileLoading).not.toContain('edit');
    expect(buttonsWhileLoading).not.toContain('delete');
    expect(api.entityDelete).not.toHaveBeenCalled();

    await act(async () => {
      resolveGet({ email: 'row-1@example.com' });
    });

    const buttonsAfterLoad = Array.from(container.querySelectorAll('button')).map((b) => b.textContent);
    expect(buttonsAfterLoad).toContain('edit');
    expect(buttonsAfterLoad).toContain('delete');
  });
});
