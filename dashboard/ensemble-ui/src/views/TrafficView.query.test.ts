import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import TrafficView from './TrafficView';
import { api } from '../api/client';
import * as sse from '../api/sse';
import type { Hop, Topology } from '../api/types';

// End-to-end: typing a structured query into the (enhanced) search box
// actually drives HopTable's rows, not just trafficFilter's own unit tests.

const OK: Hop = {
  schema: 'hop.v1',
  seq: 1,
  to: 'catalog',
  method: 'GET',
  path: '/v1/products',
  status: 200,
  t: { start: '2026-08-21T00:00:00.000Z', doneMs: 20 },
};
const NOT_FOUND: Hop = {
  schema: 'hop.v1',
  seq: 2,
  to: 'catalog',
  method: 'GET',
  path: '/v1/widgets',
  status: 404,
  t: { start: '2026-08-21T00:00:00.010Z', doneMs: 500 },
};
const SERVER_ERR: Hop = {
  schema: 'hop.v1',
  seq: 3,
  to: 'catalog',
  method: 'POST',
  path: '/v1/orders',
  status: 500,
  t: { start: '2026-08-21T00:00:00.020Z', doneMs: 10 },
};

const EMPTY_TOPOLOGY: Topology = { nodes: [], edges: [] };

const nativeValueSetter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value')!.set!;

function type(input: HTMLInputElement, text: string) {
  nativeValueSetter.call(input, text);
  input.dispatchEvent(new Event('input', { bubbles: true }));
}

function press(input: HTMLInputElement, key: string) {
  input.dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true }));
}

describe('TrafficView: query filter', () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    container = document.createElement('div');
    document.body.appendChild(container);
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    vi.restoreAllMocks();
  });

  it('renders method/path as separate columns', async () => {
    vi.spyOn(api, 'traffic').mockResolvedValue([OK]);
    vi.spyOn(api, 'topology').mockResolvedValue(EMPTY_TOPOLOGY);
    vi.spyOn(sse, 'subscribeHops').mockReturnValue(() => {});

    root = createRoot(container);
    await act(async () => {
      root.render(createElement(TrafficView));
    });

    expect(container.querySelector('td.hop-table__method')?.textContent).toBe('GET');
    expect(container.querySelector('td.hop-table__path')?.textContent).toBe('/v1/products');
  });

  it('filters live as a status: token is typed, before it commits to a pill', async () => {
    vi.spyOn(api, 'traffic').mockResolvedValue([OK, NOT_FOUND, SERVER_ERR]);
    vi.spyOn(api, 'topology').mockResolvedValue(EMPTY_TOPOLOGY);
    vi.spyOn(sse, 'subscribeHops').mockReturnValue(() => {});

    root = createRoot(container);
    await act(async () => {
      root.render(createElement(TrafficView));
    });
    expect(container.querySelectorAll('tbody tr')).toHaveLength(3);

    const input = container.querySelector('.query-filter__input') as HTMLInputElement;
    await act(async () => type(input, 'status:4xx'));
    expect(container.querySelectorAll('tbody tr')).toHaveLength(1); // NOT_FOUND (404) only, not SERVER_ERR (500)
  });

  it('commits a pill on Tab and keeps filtering after the draft clears', async () => {
    vi.spyOn(api, 'traffic').mockResolvedValue([OK, NOT_FOUND, SERVER_ERR]);
    vi.spyOn(api, 'topology').mockResolvedValue(EMPTY_TOPOLOGY);
    vi.spyOn(sse, 'subscribeHops').mockReturnValue(() => {});

    root = createRoot(container);
    await act(async () => {
      root.render(createElement(TrafficView));
    });

    const input = container.querySelector('.query-filter__input') as HTMLInputElement;
    await act(async () => type(input, 'status:404'));
    await act(async () => press(input, 'Tab'));

    expect(input.value).toBe('');
    expect(container.querySelector('.query-filter__pill')?.textContent).toContain('status:404');
    expect(container.querySelectorAll('tbody tr')).toHaveLength(1);
    expect(container.querySelector('td.hop-table__path')?.textContent).toBe('/v1/widgets');
  });

  it('combines a pill with a numeric comparison: status:4xx AND done>100ms', async () => {
    vi.spyOn(api, 'traffic').mockResolvedValue([OK, NOT_FOUND, SERVER_ERR]);
    vi.spyOn(api, 'topology').mockResolvedValue(EMPTY_TOPOLOGY);
    vi.spyOn(sse, 'subscribeHops').mockReturnValue(() => {});

    root = createRoot(container);
    await act(async () => {
      root.render(createElement(TrafficView));
    });

    const input = container.querySelector('.query-filter__input') as HTMLInputElement;
    await act(async () => type(input, 'status:4xx'));
    await act(async () => press(input, 'Tab'));
    await act(async () => type(input, 'done>100ms'));
    await act(async () => press(input, 'Tab'));

    // status:4xx matches NOT_FOUND(404) and SERVER_ERR(500); done>100ms
    // only NOT_FOUND(500ms) — AND across the two pills leaves just one row.
    expect(container.querySelectorAll('tbody tr')).toHaveLength(1);
    expect(container.querySelector('td.hop-table__path')?.textContent).toBe('/v1/widgets');
  });

  it('falls back to a free-text substring match for plain words', async () => {
    vi.spyOn(api, 'traffic').mockResolvedValue([OK, NOT_FOUND, SERVER_ERR]);
    vi.spyOn(api, 'topology').mockResolvedValue(EMPTY_TOPOLOGY);
    vi.spyOn(sse, 'subscribeHops').mockReturnValue(() => {});

    root = createRoot(container);
    await act(async () => {
      root.render(createElement(TrafficView));
    });

    const input = container.querySelector('.query-filter__input') as HTMLInputElement;
    await act(async () => type(input, 'orders'));
    expect(container.querySelectorAll('tbody tr')).toHaveLength(1);
    expect(container.querySelector('td.hop-table__path')?.textContent).toBe('/v1/orders');
  });
});
