import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import ServicesView from './ServicesView';
import { api } from '../api/client';
import type { ServiceState, Topology } from '../api/types';

// Supervision states (audit-hardening group 5): a process that ended on its own renders as
// "exited" (clean) or "crashed" (non-zero/signal) with the exit detail in the badge — both
// distinct from operator "stopped" — and every row offers a log pane fed by the SSE follow
// (GET /api/services/{name}/logs/stream).

/** Minimal EventSource stand-in: jsdom has none, and subscribeServiceLog needs only
    addEventListener('log', ...) + close(). Frames are pushed by hand via emit(). */
class FakeEventSource {
  static instances: FakeEventSource[] = [];
  url: string;
  onerror: (() => void) | null = null;
  private listeners = new Map<string, ((evt: MessageEvent<string>) => void)[]>();

  constructor(url: string) {
    this.url = url;
    FakeEventSource.instances.push(this);
  }

  addEventListener(name: string, cb: (evt: MessageEvent<string>) => void) {
    this.listeners.set(name, [...(this.listeners.get(name) ?? []), cb]);
  }

  emit(name: string, data: string) {
    for (const cb of this.listeners.get(name) ?? []) cb({ data } as MessageEvent<string>);
  }

  close() {}
}

const TOPOLOGY: Topology = { nodes: [], edges: [] };

const SERVICES: ServiceState[] = [
  { name: 'web', status: 'crashed', placement: 'native', exitCode: 1, lastErr: 'panic: boom' },
  { name: 'worker', status: 'exited', placement: 'native', exitCode: 0 },
];

function rowFor(container: HTMLDivElement, name: string): Element | undefined {
  return Array.from(container.querySelectorAll('.services-table__row')).find(
    (r) => r.querySelector('.services-table__name')?.textContent === name,
  );
}

describe('ServicesView: supervision states and log pane', () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    container = document.createElement('div');
    document.body.appendChild(container);
    FakeEventSource.instances = [];
    vi.stubGlobal('EventSource', FakeEventSource);
    vi.spyOn(api, 'topology').mockResolvedValue(TOPOLOGY);
    vi.spyOn(api, 'wiringWarnings').mockResolvedValue([]);
    vi.spyOn(api, 'gatewayStatus').mockResolvedValue([]);
    vi.spyOn(api, 'status').mockResolvedValue(SERVICES);
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  async function render() {
    root = createRoot(container);
    await act(async () => {
      root.render(createElement(ServicesView));
    });
  }

  it('renders crashed with its exit code and exited distinctly, both startable', async () => {
    await render();

    const web = rowFor(container, 'web');
    const worker = rowFor(container, 'worker');
    expect(web?.textContent).toContain('crashed (exit 1)');
    expect(worker?.textContent).toContain('exited (exit 0)');
    // Both are "not running": the row offers Start, not Restart/Stop.
    for (const row of [web, worker]) {
      const labels = Array.from(row?.querySelectorAll('button') ?? []).map((b) => b.textContent);
      expect(labels).toContain('Start');
      expect(labels).not.toContain('Stop');
    }
    // The crash's log tail (lastErr) is one hover away on the status badge.
    expect(web?.querySelector('span[title="panic: boom"]')).toBeTruthy();
  });

  it('opens a per-service log pane following the SSE stream', async () => {
    await render();

    const logsButton = Array.from(rowFor(container, 'web')?.querySelectorAll('button') ?? []).find(
      (b) => b.textContent === 'Logs',
    );
    expect(logsButton).toBeTruthy();
    await act(async () => {
      logsButton!.click();
    });

    expect(FakeEventSource.instances.length).toBe(1);
    expect(FakeEventSource.instances[0].url).toBe('/api/services/web/logs/stream');

    await act(async () => {
      FakeEventSource.instances[0].emit('log', 'boot line 1\nboot line 2');
    });
    const pane = container.querySelector('.services-table__log');
    expect(pane?.textContent).toContain('boot line 1');
    expect(pane?.textContent).toContain('boot line 2');

    // Toggling again closes the pane.
    await act(async () => {
      Array.from(rowFor(container, 'web')?.querySelectorAll('button') ?? [])
        .find((b) => b.textContent === 'Hide logs')!
        .click();
    });
    expect(container.querySelector('.services-table__log')).toBeNull();
  });
});
