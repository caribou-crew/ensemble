import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import ServicesView from './ServicesView';
import { api } from '../api/client';
import type { ServiceState, Topology } from '../api/types';

// config.Service/Variant `kind:` badges a Services-tab row (e.g. "stub", "mock"); unset
// displays as "service". Named kind, not type, to avoid colliding with config.Database's
// `type:` (a validated engine enum — postgres, redis, etc). Stubs (cfg.Stubs) never had rows
// here at all — they only ever showed up in Topology — so this also merges them in,
// auto-badged "stub" from topology's existing category field, with dashes for the columns
// that don't apply to them.

const TOPOLOGY: Topology = {
  nodes: [
    { name: 'payments-stub', category: 'stub', status: 'static' },
    { name: 'public', category: 'gateway', status: 'static', entry: true },
    { name: 'web', category: 'service', status: 'healthy' },
  ],
  edges: [],
};

const SERVICES: ServiceState[] = [
  { name: 'web', status: 'healthy', placement: 'native', kind: 'mock' },
  { name: 'api', status: 'healthy', placement: 'docker' }, // no kind: displays "service"
];

function rowNames(container: HTMLDivElement): string[] {
  return Array.from(container.querySelectorAll('.services-table__name')).map((el) => el.textContent ?? '');
}

describe('ServicesView: entity kind', () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    container = document.createElement('div');
    document.body.appendChild(container);
    vi.spyOn(api, 'topology').mockResolvedValue(TOPOLOGY);
    vi.spyOn(api, 'wiringWarnings').mockResolvedValue([]);
    vi.spyOn(api, 'gatewayStatus').mockResolvedValue([]);
    vi.spyOn(api, 'status').mockResolvedValue(SERVICES);
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    vi.restoreAllMocks();
  });

  it('badges a configured kind, defaulting to "service" when unset', async () => {
    root = createRoot(container);
    await act(async () => {
      root.render(createElement(ServicesView));
    });

    const rows = Array.from(container.querySelectorAll('.services-table__row'));
    const web = rows.find((r) => r.querySelector('.services-table__name')?.textContent === 'web');
    const apiRow = rows.find((r) => r.querySelector('.services-table__name')?.textContent === 'api');
    expect(web?.textContent).toContain('mock');
    expect(apiRow?.textContent).toContain('service');
  });

  it('renders a stub from cfg.Stubs as its own row, badged "stub", with dashes for the rest', async () => {
    root = createRoot(container);
    await act(async () => {
      root.render(createElement(ServicesView));
    });

    expect(rowNames(container)).toContain('payments-stub');
    const rows = Array.from(container.querySelectorAll('.services-table__row'));
    const stubRow = rows.find((r) => r.querySelector('.services-table__name')?.textContent === 'payments-stub');
    expect(stubRow, 'expected a row for the stub').toBeTruthy();
    expect(stubRow!.textContent).toContain('stub');
    // No lifecycle actions for a stub row (start/restart/stop/flip don't apply).
    const actionsCell = stubRow!.querySelector('.services-table__actions');
    expect(actionsCell?.querySelector('button')).toBeFalsy();
  });

  it('renders a gateway from cfg.Gateways as its own row, badged "gateway", with dashes for the rest', async () => {
    root = createRoot(container);
    await act(async () => {
      root.render(createElement(ServicesView));
    });

    expect(rowNames(container)).toContain('public');
    const rows = Array.from(container.querySelectorAll('.services-table__row'));
    const gatewayRow = rows.find((r) => r.querySelector('.services-table__name')?.textContent === 'public');
    expect(gatewayRow, 'expected a row for the gateway').toBeTruthy();
    expect(gatewayRow!.textContent).toContain('gateway');
    // No lifecycle actions for a gateway row (start/restart/stop/flip don't apply).
    const actionsCell = gatewayRow!.querySelector('.services-table__actions');
    expect(actionsCell?.querySelector('button')).toBeFalsy();
  });
});
