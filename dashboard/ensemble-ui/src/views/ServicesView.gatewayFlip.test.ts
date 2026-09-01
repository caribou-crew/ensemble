import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import ServicesView from './ServicesView';
import { api } from '../api/client';
import type { GatewayStatus, ServiceState, Topology } from '../api/types';

// A gateway's flip control always offers "local" plus every declared upstream — never a
// plain 2-choice button the way a service can, since a gateway with any upstreams at all has
// at least 2 options (local + 1) and typically more.

const TOPOLOGY: Topology = {
  nodes: [
    { name: 'public', category: 'gateway', status: 'static', entry: true, upstreams: ['qa', 'sandbox'] },
    { name: 'internal', category: 'gateway', status: 'static', entry: true },
  ],
  edges: [],
};

const SERVICES: ServiceState[] = [];
const GATEWAYS: GatewayStatus[] = [
  { name: 'public', activeTarget: 'local' },
  { name: 'internal', activeTarget: 'local' },
];

function rowFor(container: HTMLDivElement, name: string): Element {
  const rows = Array.from(container.querySelectorAll('.services-table__row'));
  const row = rows.find((r) => r.querySelector('.services-table__name')?.textContent === name);
  if (!row) throw new Error(`no row for ${name}`);
  return row;
}

describe('ServicesView: gateway flip control', () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    container = document.createElement('div');
    document.body.appendChild(container);
    vi.spyOn(api, 'topology').mockResolvedValue(TOPOLOGY);
    vi.spyOn(api, 'wiringWarnings').mockResolvedValue([]);
    vi.spyOn(api, 'status').mockResolvedValue(SERVICES);
    vi.spyOn(api, 'gatewayStatus').mockResolvedValue(GATEWAYS);
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    vi.restoreAllMocks();
  });

  it('offers local + every declared upstream for a gateway with upstreams', async () => {
    root = createRoot(container);
    await act(async () => {
      root.render(createElement(ServicesView));
    });

    const row = rowFor(container, 'public');
    const select = row.querySelector('.services-table__actions select') as HTMLSelectElement;
    expect(select).toBeTruthy();
    const optionValues = Array.from(select.options).map((o) => o.value).filter(Boolean);
    expect(optionValues.sort()).toEqual(['qa', 'sandbox']);
  });

  it('calls api.flipGateway with the selected target', async () => {
    const flipGateway = vi.spyOn(api, 'flipGateway').mockResolvedValue([{ name: 'public', activeTarget: 'qa' }]);
    root = createRoot(container);
    await act(async () => {
      root.render(createElement(ServicesView));
    });

    const row = rowFor(container, 'public');
    const select = row.querySelector('.services-table__actions select') as HTMLSelectElement;
    await act(async () => {
      select.value = 'qa';
      select.dispatchEvent(new Event('change', { bubbles: true }));
    });

    expect(flipGateway).toHaveBeenCalledWith('public', 'qa');
  });

  it('renders no flip control for a gateway with zero declared upstreams', async () => {
    root = createRoot(container);
    await act(async () => {
      root.render(createElement(ServicesView));
    });

    const row = rowFor(container, 'internal');
    const actionsCell = row.querySelector('.services-table__actions')!;
    expect(actionsCell.querySelector('select')).toBeFalsy();
    expect(actionsCell.querySelector('button')).toBeFalsy();
  });

  it('renders the gateway\'s current active target in the placement column', async () => {
    vi.spyOn(api, 'gatewayStatus').mockResolvedValue([
      { name: 'public', activeTarget: 'qa' },
      { name: 'internal', activeTarget: 'local' },
    ]);
    root = createRoot(container);
    await act(async () => {
      root.render(createElement(ServicesView));
    });

    const row = rowFor(container, 'public');
    expect(row.textContent).toContain('qa');
  });
});
