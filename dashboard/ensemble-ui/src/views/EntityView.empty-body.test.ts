import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import EntityView from './EntityView';
import { api } from '../api/client';
import type { EntityInfo } from '../api/types';

// entityList/entityGet are the only two raw-passthrough client wrappers: they return the
// upstream's body as unknown, with no `.then(r => r.rows)`-style unwrap to reject on a
// missing field. So a 200 with an empty (or unparseable) body resolves to undefined —
// which used to be the same value the views used to mean "not fetched yet", showing a
// permanent spinner with no error and feeding undefined into a controlled <textarea>.
// Loading is now tracked separately; an empty body must say so.

const ENTITIES: EntityInfo[] = [{ name: 'users', id: 'id' }];

describe('EntityView: empty 200 response body', () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    container = document.createElement('div');
    document.body.appendChild(container);
    vi.spyOn(api, 'entities').mockResolvedValue(ENTITIES);
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    vi.restoreAllMocks();
    window.history.replaceState(null, '', '/');
  });

  async function render(path: string) {
    window.history.replaceState(null, '', path);
    root = createRoot(container);
    await act(async () => {
      root.render(createElement(EntityView));
    });
  }

  it('reports an empty list body instead of spinning forever', async () => {
    vi.spyOn(api, 'entityList').mockResolvedValue(undefined);

    await render('/?entity=users');

    expect(container.querySelector('.entity-list__loading')).toBeNull();
    expect(container.innerHTML).toContain('empty response body');
  });

  it('reports an empty detail body instead of spinning forever', async () => {
    vi.spyOn(api, 'entityList').mockResolvedValue([]);
    vi.spyOn(api, 'entityGet').mockResolvedValue(undefined);

    await render('/?entity=users&id=7');

    expect(container.innerHTML).toContain('empty response body');
    // No edit affordance for a body that does not exist — editing it would PUT the
    // string "undefined" back at the upstream.
    const buttons = Array.from(container.querySelectorAll('button')).map((b) => b.textContent);
    expect(buttons).not.toContain('edit');
  });

  it('still renders a real body normally', async () => {
    // Control: proves the two assertions above are detecting the empty-body path and not
    // simply a view that never renders anything.
    vi.spyOn(api, 'entityList').mockResolvedValue([]);
    vi.spyOn(api, 'entityGet').mockResolvedValue({ email: 'real@example.com' });

    await render('/?entity=users&id=7');

    expect(container.innerHTML).toContain('real@example.com');
    expect(container.innerHTML).not.toContain('empty response body');
  });
});
