import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import EntityView from './EntityView';
import { api } from '../api/client';
import { writeParams } from '../urlState';
import type { EntityInfo } from '../api/types';

describe('F12 observability probe', () => {
  let container: HTMLDivElement;
  let root: Root;
  beforeEach(() => {
    container = document.createElement('div');
    document.body.appendChild(container);
    vi.spyOn(api, 'entities').mockResolvedValue([{ name: 'users', id: 'id' }] as EntityInfo[]);
    vi.spyOn(api, 'entityList').mockResolvedValue([]);
  });
  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    vi.restoreAllMocks();
    window.history.replaceState(null, '', '/');
  });

  it('samples the textarea across an id switch while editing', async () => {
    let resolve2!: (v: unknown) => void;
    const row2 = new Promise<unknown>((r) => { resolve2 = r; });
    vi.spyOn(api, 'entityGet').mockImplementation((_n: string, id: string) => {
      if (id === '1') return Promise.resolve({ id: 1, email: 'a@b.c' });
      return row2 as Promise<unknown>;
    });
    writeParams({ entity: 'users', id: '1' });
    root = createRoot(container);
    await act(async () => { root.render(createElement(EntityView)); });

    const editBtn = Array.from(container.querySelectorAll('button')).find((b) => b.textContent === 'edit');
    await act(async () => { editBtn!.click(); });
    const ta = () => container.querySelector('textarea');
    console.log('EDITING row1 textarea:', JSON.stringify(ta()?.value));

    await act(async () => { writeParams({ entity: 'users', id: '2' }); window.dispatchEvent(new PopStateEvent('popstate')); });
    console.log('MID-LOAD textarea present?', ta() !== null, 'value:', JSON.stringify(ta()?.value));
    console.log('MID-LOAD html snippet:', container.querySelector('.entity-detail')?.innerHTML.slice(0, 200));

    await act(async () => { resolve2({ id: 2, email: 'x@y.z' }); });
    console.log('AFTER row2 textarea:', JSON.stringify(ta()?.value));
    expect(true).toBe(true);
  });
});
