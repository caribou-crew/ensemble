import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import ServicesView from './ServicesView';
import { api } from '../api/client';
import type { ServiceState, Topology } from '../api/types';

const TOPOLOGY: Topology = { nodes: [], edges: [] };

const SERVICES: ServiceState[] = [
  { name: 'web', status: 'healthy', placement: 'native', rssKB: 2048 },
  { name: 'api', status: 'healthy', placement: 'docker', rssKB: 512 },
  { name: 'db', status: 'stopped', placement: 'native' }, // no rssKB sample
];

function names(container: HTMLDivElement): string[] {
  return Array.from(container.querySelectorAll('.services-table__name')).map((el) => el.textContent ?? '');
}

function headerButton(container: HTMLDivElement, label: string): HTMLButtonElement {
  const btn = Array.from(container.querySelectorAll('thead button')).find((b) => b.textContent?.startsWith(label));
  if (!btn) throw new Error(`no sortable header found for ${label}`);
  return btn as HTMLButtonElement;
}

describe('ServicesView: column sorting', () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    container = document.createElement('div');
    document.body.appendChild(container);
    vi.spyOn(api, 'topology').mockResolvedValue(TOPOLOGY);
    vi.spyOn(api, 'wiringWarnings').mockResolvedValue([]);
    vi.spyOn(api, 'status').mockResolvedValue(SERVICES);
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    vi.restoreAllMocks();
  });

  it('is unsorted (declared order) until a header is clicked, then toggles asc/desc', async () => {
    root = createRoot(container);
    await act(async () => {
      root.render(createElement(ServicesView));
    });

    expect(names(container)).toEqual(['web', 'api', 'db']);

    await act(async () => {
      headerButton(container, 'name').click();
    });
    expect(names(container)).toEqual(['api', 'db', 'web']);

    await act(async () => {
      headerButton(container, 'name').click();
    });
    expect(names(container)).toEqual(['web', 'db', 'api']);
  });

  it('sorts rss numerically, with a service missing a sample landing at the low end', async () => {
    root = createRoot(container);
    await act(async () => {
      root.render(createElement(ServicesView));
    });

    await act(async () => {
      headerButton(container, 'rss').click();
    });
    expect(names(container)).toEqual(['db', 'api', 'web']);

    await act(async () => {
      headerButton(container, 'rss').click();
    });
    expect(names(container)).toEqual(['web', 'api', 'db']);
  });

  it('switching sort column resets to ascending on the new column', async () => {
    root = createRoot(container);
    await act(async () => {
      root.render(createElement(ServicesView));
    });

    await act(async () => {
      headerButton(container, 'name').click(); // asc by name: api, db, web
    });
    await act(async () => {
      headerButton(container, 'name').click(); // desc by name: web, db, api
    });
    expect(names(container)).toEqual(['web', 'db', 'api']);

    await act(async () => {
      headerButton(container, 'name').click(); // back to asc
    });
    await act(async () => {
      headerButton(container, 'placement').click(); // new column starts ascending
    });
    // Sorting always re-derives from the original (declared) order — web, api, db — not
    // from whatever order the previous sort left on screen. placement asc: docker (api)
    // first, then native ties break in that declared order: web, db.
    expect(names(container)).toEqual(['api', 'web', 'db']);
  });
});
