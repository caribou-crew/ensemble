import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import ServicesView from './ServicesView';
import { api } from '../api/client';
import type { ServiceState, Topology } from '../api/types';

// Services tab freshness badges — see openspec/changes/service-freshness. clean/unset
// renders no badge, a nonzero behind-count renders an amber badge naming it, and a
// never-checked-or-failed state renders "unknown" distinctly from clean.

const TOPOLOGY: Topology = { nodes: [], edges: [] };

const SERVICES: ServiceState[] = [
  { name: 'clean', status: 'healthy', placement: 'native' }, // no freshness: never eligible
  {
    name: 'behind',
    status: 'healthy',
    placement: 'native',
    freshness: { branch: 'feature', behindBranch: 3, behindDefault: 7, defaultBranch: 'main', checkedAt: '2026-08-27T10:00:00Z' },
  },
  {
    name: 'uptodate',
    status: 'healthy',
    placement: 'native',
    freshness: { branch: 'main', behindBranch: 0, behindDefault: 0, defaultBranch: 'main', checkedAt: '2026-08-27T10:00:00Z' },
  },
  {
    name: 'unknown',
    status: 'healthy',
    placement: 'native',
    freshness: { branch: '', behindBranch: 0, behindDefault: 0, defaultBranch: 'main', error: 'git fetch origin: network unreachable' },
  },
];

function rowFor(container: HTMLDivElement, name: string): Element | undefined {
  const rows = Array.from(container.querySelectorAll('.services-table__row'));
  return rows.find((r) => r.querySelector('.services-table__name')?.textContent === name);
}

describe('ServicesView: freshness', () => {
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

  it('renders no badge for a service with no freshness state', async () => {
    root = createRoot(container);
    await act(async () => {
      root.render(createElement(ServicesView));
    });
    const row = rowFor(container, 'clean');
    expect(row?.querySelectorAll('.ds-badge--amber').length).toBe(0);
  });

  it('renders no badge for a service that is up to date', async () => {
    root = createRoot(container);
    await act(async () => {
      root.render(createElement(ServicesView));
    });
    const row = rowFor(container, 'uptodate');
    expect(row?.querySelectorAll('.ds-badge--amber').length).toBe(0);
  });

  it('renders both behind-badges when both counts are nonzero', async () => {
    root = createRoot(container);
    await act(async () => {
      root.render(createElement(ServicesView));
    });
    const row = rowFor(container, 'behind');
    expect(row?.textContent).toContain('↓3');
    expect(row?.textContent).toContain('main ↓7');
  });

  it('renders an unknown badge, distinct from clean, for a never-checked/failed state', async () => {
    root = createRoot(container);
    await act(async () => {
      root.render(createElement(ServicesView));
    });
    const row = rowFor(container, 'unknown');
    expect(row?.textContent).toContain('unknown');
    expect(row?.querySelectorAll('.ds-badge--amber').length).toBe(0);
  });

  it('triggers POST /api/freshness/check and refreshes when "Check freshness" is clicked', async () => {
    const checkSpy = vi
      .spyOn(api, 'freshnessCheck')
      .mockResolvedValue({ services: SERVICES, configured: true });
    root = createRoot(container);
    await act(async () => {
      root.render(createElement(ServicesView));
    });

    const button = Array.from(container.querySelectorAll('button')).find((b) => b.textContent === 'Check freshness');
    expect(button, 'expected a "Check freshness" button').toBeTruthy();
    await act(async () => {
      button!.click();
    });
    expect(checkSpy).toHaveBeenCalledTimes(1);
  });

  it('opens a results drawer explaining what the check did', async () => {
    vi.spyOn(api, 'freshnessCheck').mockResolvedValue({ services: SERVICES, configured: true });
    root = createRoot(container);
    await act(async () => {
      root.render(createElement(ServicesView));
    });

    const button = Array.from(container.querySelectorAll('button')).find((b) => b.textContent === 'Check freshness');
    await act(async () => {
      button!.click();
    });

    const body = container.querySelector('.freshness-drawer__body');
    expect(body, 'expected a freshness results drawer').toBeTruthy();
    expect(body!.textContent).toContain('Checked 3 eligible services');
    expect(body!.textContent).toContain('unknown: FAILED');
    expect(body!.textContent).toContain('1 service not eligible');
    expect(body!.textContent).toContain('clean');
  });

  it('explains itself when freshness: is not configured at all', async () => {
    vi.spyOn(api, 'freshnessCheck').mockResolvedValue({ services: SERVICES, configured: false });
    root = createRoot(container);
    await act(async () => {
      root.render(createElement(ServicesView));
    });

    const button = Array.from(container.querySelectorAll('button')).find((b) => b.textContent === 'Check freshness');
    await act(async () => {
      button!.click();
    });

    const body = container.querySelector('.freshness-drawer__body');
    expect(body!.textContent).toContain("isn't configured");
  });
});
