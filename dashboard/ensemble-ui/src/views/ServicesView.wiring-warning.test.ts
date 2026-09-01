import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import ServicesView from './ServicesView';
import { api } from '../api/client';
import type { ServiceState, Topology, WiringWarning } from '../api/types';

// proxy-wiring-validation task 3.4: GET /api/status's `warnings` field badges the
// REFERENCING service (the one whose env: is mis-wired), not the target — with the
// warning's message as the badge's tooltip.

const TOPOLOGY: Topology = {
  nodes: [
    { name: 'edge', category: 'service', status: 'healthy' },
    { name: 'catalog', category: 'service', status: 'healthy' },
  ],
  edges: [],
};

const SERVICES: ServiceState[] = [
  { name: 'edge', status: 'healthy', placement: 'native', port: 8080, proxyPort: 9080 },
  { name: 'catalog', status: 'healthy', placement: 'native', port: 8081, proxyPort: 9081 },
];

const WARNING: WiringWarning = {
  service: 'edge',
  env: 'CATALOG_URL',
  target: 'catalog',
  port: 8081,
  proxyPort: 9081,
  message: "edge's CATALOG_URL points at catalog's real port 8081; hops bypass capture — use proxy port 9081 instead",
};

function rowFor(container: HTMLDivElement, name: string): Element | undefined {
  return Array.from(container.querySelectorAll('.services-table__row')).find(
    (r) => r.querySelector('.services-table__name')?.textContent?.trim().startsWith(name),
  );
}

describe('ServicesView: wiring warning badge', () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    container = document.createElement('div');
    document.body.appendChild(container);
    vi.spyOn(api, 'topology').mockResolvedValue(TOPOLOGY);
    vi.spyOn(api, 'status').mockResolvedValue(SERVICES);
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    vi.restoreAllMocks();
  });

  it('badges the REFERENCING service with the message as a tooltip, leaving the target untouched', async () => {
    vi.spyOn(api, 'wiringWarnings').mockResolvedValue([WARNING]);

    root = createRoot(container);
    await act(async () => {
      root.render(createElement(ServicesView));
    });

    const edgeRow = rowFor(container, 'edge');
    const catalogRow = rowFor(container, 'catalog');
    expect(edgeRow, 'expected a row for edge').toBeTruthy();
    expect(catalogRow, 'expected a row for catalog').toBeTruthy();

    const badge = edgeRow!.querySelector('.services-table__wiring-warning');
    expect(badge, 'expected a wiring-warning badge on edge').toBeTruthy();
    expect(badge!.textContent).toContain('wiring');
    expect(badge!.getAttribute('title')).toBe(WARNING.message);

    expect(catalogRow!.querySelector('.services-table__wiring-warning')).toBeFalsy();
  });

  it('renders no badge when there are no wiring warnings', async () => {
    vi.spyOn(api, 'wiringWarnings').mockResolvedValue([]);

    root = createRoot(container);
    await act(async () => {
      root.render(createElement(ServicesView));
    });

    expect(container.querySelector('.services-table__wiring-warning')).toBeFalsy();
  });
});
