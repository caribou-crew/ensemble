import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import EntityView from './EntityView';
import { api } from '../api/client';
import { readParam } from '../urlState';
import type { EntityInfo } from '../api/types';

// Regression test for final-review-phase-3.md's Parked #3: the ?entity=<invalid> clearing
// effect added in 8d83f8e (EntityView.tsx:413-419) had no test of its own, and I2 shows its
// sibling view built the same commit shipped WITHOUT the equivalent fix — demonstrating this
// invariant is not merely untested but actively at risk of silent regression. Pinned here
// alongside InspectorView's mirrored fix (InspectorView.url-clear.test.ts).

const ENTITIES: EntityInfo[] = [{ name: 'users', id: 'id' }];

describe('EntityView: stale ?entity= (and dependent ?id=) clearing', () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    container = document.createElement('div');
    document.body.appendChild(container);
    vi.spyOn(api, 'entities').mockResolvedValue(ENTITIES);
    vi.spyOn(api, 'entityList').mockResolvedValue([]);
    // EntityDetail briefly mounts with the stale ?id= on the FIRST render commit — its
    // fetch effect (a child) runs before EntityView's own clearing effect (the parent) does,
    // same lifecycle ordering EntityDetail's own race test relies on — so this needs a mock
    // even in the "clears" case, purely to keep that one-tick fetch from hitting the network.
    vi.spyOn(api, 'entityGet').mockResolvedValue({ id: '5' });
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    vi.restoreAllMocks();
    window.history.replaceState(null, '', '/');
  });

  it('clears ?entity= and its dependent ?id= when the entity names nothing', async () => {
    window.history.replaceState(null, '', '/?view=entities&entity=stale&id=5');
    root = createRoot(container);
    await act(async () => {
      root.render(createElement(EntityView));
    });

    expect(readParam('entity'), '?entity=stale names nothing and must be cleared').toBeNull();
    expect(readParam('id'), '?id= is only meaningful relative to ?entity= and must clear with it').toBeNull();
    expect(container.textContent).toContain('users');
  });

  it('leaves a VALID ?entity=&?id= alone', async () => {
    window.history.replaceState(null, '', '/?view=entities&entity=users&id=5');
    root = createRoot(container);
    await act(async () => {
      root.render(createElement(EntityView));
    });

    expect(readParam('entity')).toBe('users');
    expect(readParam('id')).toBe('5');
  });
});
