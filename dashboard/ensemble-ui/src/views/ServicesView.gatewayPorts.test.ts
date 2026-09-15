import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import ServicesView from './ServicesView';
import { api } from '../api/client';
import type { GatewayStatus, ServiceState, Topology } from '../api/types';

// Gateways and stubs are listeners, not supervised processes, so most of the numeric
// columns genuinely don't apply to them — but the two that DO, a gateway's listen port and
// how long its listener has been bound, used to render as dashes alongside the ones that
// don't. These lock in which cells carry real values and which stay empty on purpose.

const TOPOLOGY: Topology = {
  nodes: [
    { name: 'public', category: 'gateway', status: 'static', entry: true, port: 9000, upstreams: ['qa'] },
    { name: 'unbound', category: 'gateway', status: 'static', entry: true, port: 9100 },
    { name: 'payments-stub', category: 'stub', status: 'static', port: 9300 },
  ],
  edges: [],
};

const SERVICES: ServiceState[] = [];

/** Column order matches COLUMNS + freshness + actions. */
const PORT = 5;
const PROXY = 6;
const RSS = 7;
const UPTIME = 8;

function cells(container: HTMLDivElement, name: string): HTMLTableCellElement[] {
  const rows = Array.from(container.querySelectorAll('.services-table__row'));
  const row = rows.find((r) => r.querySelector('.services-table__name')?.textContent === name);
  if (!row) throw new Error(`no row for ${name}`);
  return Array.from(row.querySelectorAll('td'));
}

describe('ServicesView: gateway and stub ports/uptime', () => {
  let container: HTMLDivElement;
  let root: Root;

  function mountWith(gateways: GatewayStatus[]) {
    vi.spyOn(api, 'gatewayStatus').mockResolvedValue(gateways);
    root = createRoot(container);
    return act(async () => {
      root.render(createElement(ServicesView));
    });
  }

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

  it("shows a gateway's declared listen port in the port column", async () => {
    await mountWith([{ name: 'public', activeTarget: 'local' }]);
    expect(cells(container, 'public')[PORT].textContent).toBe('9000');
  });

  it("shows a stub's declared listen port in the port column", async () => {
    await mountWith([]);
    expect(cells(container, 'payments-stub')[PORT].textContent).toBe('9300');
  });

  it('shows uptime measured from the listener bind time', async () => {
    const boundAt = new Date(Date.now() - 2 * 60 * 60 * 1000 - 14 * 60 * 1000).toISOString();
    await mountWith([{ name: 'public', activeTarget: 'local', boundAt }]);
    expect(cells(container, 'public')[UPTIME].textContent).toBe('2h 14m');
  });

  // The fail-closed half of the pair: an unbound gateway must not read as "bound since the
  // epoch" (a 55-year uptime) just because boundAt is absent from the payload.
  it('shows no uptime for a gateway that is not bound', async () => {
    await mountWith([{ name: 'unbound', activeTarget: 'local' }]);
    expect(cells(container, 'unbound')[UPTIME].textContent).toBe('—');
  });

  // A gateway configured but missing from the status payload entirely — same fail-closed
  // reading as an explicitly absent boundAt.
  it('shows no uptime for a gateway absent from the gateway status payload', async () => {
    await mountWith([]);
    expect(cells(container, 'public')[UPTIME].textContent).toBe('—');
  });

  it('leaves proxy and rss empty for gateways and stubs, with the reason on hover', async () => {
    await mountWith([{ name: 'public', activeTarget: 'local' }]);
    for (const name of ['public', 'payments-stub']) {
      const row = cells(container, name);
      expect(row[PROXY].textContent).toBe('—');
      expect(row[RSS].textContent).toBe('—');
      // A bare dash reads as a missing measurement, which is the wrong story: neither cell
      // is a reading that failed, both are quantities the node does not have. The reason is
      // one hover/focus away on each.
      for (const [cell, expected] of [
        [row[PROXY], 'No separate proxy port'],
        [row[RSS], 'No resident memory of its own'],
      ] as const) {
        const tooltip = cell.querySelector('.ds-tooltip') as HTMLElement | null;
        expect(tooltip, `expected a tooltip wrapper in ${name}'s cell`).toBeTruthy();
        await act(async () => {
          tooltip!.focus();
        });
        expect(tooltip!.querySelector('.ds-tooltip__bubble')?.textContent).toContain(expected);
        await act(async () => {
          tooltip!.blur();
        });
      }
    }
  });

  it('shows no port for a gateway that declares none', async () => {
    vi.spyOn(api, 'topology').mockResolvedValue({
      nodes: [{ name: 'portless', category: 'gateway', status: 'static', entry: true }],
      edges: [],
    });
    await mountWith([]);
    expect(cells(container, 'portless')[PORT].textContent).toBe('—');
  });
});
