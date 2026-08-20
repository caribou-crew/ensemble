import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import EntityView from './EntityView';
import { api } from '../api/client';
import { writeParams } from '../urlState';
import type { EntityInfo } from '../api/types';

// Regression test for the EntityDetail load race.
//
// This is the SECOND time this bug class has appeared in this dashboard —
// TopologyView.trace-race.test.ts guards the first one. EntityDetail is not remounted
// when ?id= changes (EntityView renders it without a key prop keyed to the id), so its
// data-load effect re-fires on the SAME component instance and the previous in-flight
// request is never invalidated. A stale response landing after a newer one therefore
// overwrote both the rendered record AND draftText — and since save() PUTs draftText
// against the CURRENT id, that could write row A's JSON onto row B's record.
//
// Both arrival orders are covered, so a fix that only special-cases "old resolves after
// new" instead of ignoring any non-current response still fails.

const ENTITIES: EntityInfo[] = [{ name: 'users', id: 'id' }];

describe('EntityView: EntityDetail ?id= race', () => {
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

  async function renderSwitchingFrom1To2(resolveOrder: 'newer-first' | 'stale-first') {
    let resolve1!: (v: unknown) => void;
    let resolve2!: (v: unknown) => void;
    const row1 = new Promise<unknown>((r) => {
      resolve1 = r;
    });
    const row2 = new Promise<unknown>((r) => {
      resolve2 = r;
    });

    vi.spyOn(api, 'entityGet').mockImplementation((name: string, id: string) => {
      if (name !== 'users') throw new Error(`unexpected entity ${name}`);
      if (id === '1') return row1;
      if (id === '2') return row2;
      throw new Error(`unexpected id ${id}`);
    });

    window.history.replaceState(null, '', '/?entity=users&id=1');
    root = createRoot(container);
    await act(async () => {
      root.render(createElement(EntityView));
    });

    // Navigate to row 2 before row 1 has resolved — the real "clicked another row before
    // the first finished loading" sequence, driven through the URL exactly as a click,
    // a deep link, or browser back/forward would drive it.
    await act(async () => {
      writeParams({ id: '2' });
      window.dispatchEvent(new PopStateEvent('popstate'));
    });

    const stale = { email: 'stale-row-one@example.com' };
    const current = { email: 'current-row-two@example.com' };

    if (resolveOrder === 'newer-first') {
      await act(async () => {
        resolve2(current);
      });
      await act(async () => {
        resolve1(stale);
      });
    } else {
      await act(async () => {
        resolve1(stale);
      });
      await act(async () => {
        resolve2(current);
      });
    }
  }

  it('ignores the stale row-1 response that lands AFTER row 2 has rendered', async () => {
    await renderSwitchingFrom1To2('newer-first');

    expect(container.innerHTML).toContain('current-row-two@example.com');
    expect(container.innerHTML).not.toContain('stale-row-one@example.com');
  });

  it('renders row 2 when the stale row-1 response lands FIRST', async () => {
    await renderSwitchingFrom1To2('stale-first');

    expect(container.innerHTML).toContain('current-row-two@example.com');
    expect(container.innerHTML).not.toContain('stale-row-one@example.com');
  });

  it('does not leave a stale response in the edit draft', async () => {
    // The dangerous half of this bug: save() PUTs whatever is in draftText against the
    // CURRENT id, so a stale body left in the draft writes row 1's data onto row 2.
    await renderSwitchingFrom1To2('newer-first');

    const editButton = Array.from(container.querySelectorAll('button')).find(
      (b) => b.textContent === 'edit',
    );
    expect(editButton, 'edit button should be present once the row has loaded').toBeTruthy();

    await act(async () => {
      editButton!.click();
    });

    const draft = container.querySelector('textarea');
    expect(draft, 'edit mode should render a draft textarea').toBeTruthy();
    expect(draft!.value).toContain('current-row-two@example.com');
    expect(draft!.value).not.toContain('stale-row-one@example.com');
  });
});
