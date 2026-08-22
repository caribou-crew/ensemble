import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import EntityView from './EntityView';
import { api } from '../api/client';
import { writeParams } from '../urlState';
import type { EntityInfo } from '../api/types';

// Regression test for final review F1: EntityDetail split its single `error` state into
// `loadError` (owned by useAsync) and a local `actionError` (set by a failed delete), but
// `actionError` had no writer that ever cleared it, and EntityDetail is deliberately NOT
// remounted when ?id= changes (its own comment says so). So one failed api.entityDelete
// used to replace the record with "failed to delete" for the rest of the view's life —
// every row selected afterwards rendered the stale error instead of its own JSON, with
// `edit`/`delete` hidden. The navigate-after-failure sequence below is exactly that case.

const ENTITIES: EntityInfo[] = [{ name: 'users', id: 'id' }];

describe('EntityView: actionError clears on navigation', () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    container = document.createElement('div');
    document.body.appendChild(container);
    vi.spyOn(api, 'entities').mockResolvedValue(ENTITIES);
    vi.spyOn(api, 'entityList').mockResolvedValue([]);
    window.confirm = () => true;
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    vi.restoreAllMocks();
    window.history.replaceState(null, '', '/');
  });

  it('a failed delete on row 1 does not poison row 2 after navigating away', async () => {
    vi.spyOn(api, 'entityGet').mockImplementation((name: string, id: string) => {
      if (name !== 'users') throw new Error(`unexpected entity ${name}`);
      if (id === '1') return Promise.resolve({ email: 'row-1@example.com' });
      if (id === '2') return Promise.resolve({ email: 'row-2@example.com' });
      throw new Error(`unexpected id ${id}`);
    });
    vi.spyOn(api, 'entityDelete').mockRejectedValue(new Error('upstream 500'));

    window.history.replaceState(null, '', '/?entity=users&id=1');
    root = createRoot(container);
    await act(async () => {
      root.render(createElement(EntityView));
    });

    expect(container.innerHTML).toContain('row-1@example.com');

    const deleteButton = Array.from(container.querySelectorAll('button')).find(
      (b) => b.textContent === 'delete',
    );
    expect(deleteButton, 'delete button should be present once row 1 has loaded').toBeTruthy();

    await act(async () => {
      deleteButton!.click();
    });

    expect(container.innerHTML).toContain('failed to delete');

    // Navigate to row 2 exactly as a click, a deep link, or browser back/forward would.
    await act(async () => {
      writeParams({ id: '2' });
      window.dispatchEvent(new PopStateEvent('popstate'));
    });

    expect(container.innerHTML).toContain('row-2@example.com');
    expect(container.innerHTML).not.toContain('failed to delete');

    const buttons = Array.from(container.querySelectorAll('button')).map((b) => b.textContent);
    expect(buttons, 'edit/delete should be back once the stale action error has cleared').toContain('edit');
    expect(buttons).toContain('delete');
  });
});
