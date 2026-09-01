import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import ServicesView from './ServicesView';
import { api } from '../api/client';
import type { ServiceState, Topology } from '../api/types';

// Flip's control shape depends on how many placements a service declares (see
// ServicesView.tsx's FlipControl): a plain button for the original 2-placement
// case, a target-picking select once a service has 3 (native/docker/passthrough).

const TOPOLOGY: Topology = {
  nodes: [
    { name: 'two-way', category: 'service', status: 'healthy', placements: ['native', 'docker'] },
    {
      name: 'three-way',
      category: 'service',
      status: 'healthy',
      placements: ['native', 'docker', 'passthrough'],
    },
    { name: 'no-alt', category: 'service', status: 'healthy', placements: ['native'] },
  ],
  edges: [],
};

const SERVICES: ServiceState[] = [
  { name: 'two-way', status: 'healthy', placement: 'native' },
  { name: 'three-way', status: 'healthy', placement: 'native' },
  { name: 'no-alt', status: 'healthy', placement: 'native' },
];

function rowFor(container: HTMLDivElement, name: string): Element {
  const rows = Array.from(container.querySelectorAll('.services-table__row'));
  const row = rows.find((r) => r.querySelector('.services-table__name')?.textContent === name);
  if (!row) throw new Error(`no row for ${name}`);
  return row;
}

describe('ServicesView: flip control', () => {
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

  it('renders a single "Flip to docker" button for a 2-placement service', async () => {
    root = createRoot(container);
    await act(async () => {
      root.render(createElement(ServicesView));
    });

    const row = rowFor(container, 'two-way');
    const button = Array.from(row.querySelectorAll('button')).find((b) =>
      b.textContent?.includes('Flip to'),
    );
    expect(button?.textContent).toBe('Flip to docker');
    expect(row.querySelector('.services-table__actions select')).toBeFalsy();
  });

  it('calls api.flip with the explicit target from the 2-placement button', async () => {
    const flip = vi.spyOn(api, 'flip').mockResolvedValue(SERVICES[0]);
    root = createRoot(container);
    await act(async () => {
      root.render(createElement(ServicesView));
    });

    const row = rowFor(container, 'two-way');
    const button = Array.from(row.querySelectorAll('button')).find((b) =>
      b.textContent?.includes('Flip to'),
    ) as HTMLButtonElement;
    await act(async () => {
      button.click();
    });

    expect(flip).toHaveBeenCalledWith('two-way', 'docker');
  });

  it('renders a target-picking select for a 3-placement service, offering only the other two', async () => {
    root = createRoot(container);
    await act(async () => {
      root.render(createElement(ServicesView));
    });

    const row = rowFor(container, 'three-way');
    const select = row.querySelector('.services-table__actions select') as HTMLSelectElement;
    expect(select).toBeTruthy();
    const optionValues = Array.from(select.options).map((o) => o.value).filter(Boolean);
    expect(optionValues.sort()).toEqual(['docker', 'passthrough']);
  });

  it('calls api.flip with the selected target from the 3-placement select', async () => {
    const flip = vi.spyOn(api, 'flip').mockResolvedValue(SERVICES[1]);
    root = createRoot(container);
    await act(async () => {
      root.render(createElement(ServicesView));
    });

    const row = rowFor(container, 'three-way');
    const select = row.querySelector('.services-table__actions select') as HTMLSelectElement;
    await act(async () => {
      select.value = 'passthrough';
      select.dispatchEvent(new Event('change', { bubbles: true }));
    });

    expect(flip).toHaveBeenCalledWith('three-way', 'passthrough');
  });

  it('renders no flip control for a service with only one declared placement', async () => {
    root = createRoot(container);
    await act(async () => {
      root.render(createElement(ServicesView));
    });

    const row = rowFor(container, 'no-alt');
    const actionsCell = row.querySelector('.services-table__actions')!;
    expect(actionsCell.querySelector('select')).toBeFalsy();
    const flipButton = Array.from(actionsCell.querySelectorAll('button')).find((b) =>
      b.textContent?.includes('Flip to'),
    );
    expect(flipButton).toBeFalsy();
  });
});
