import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import HopDetail from './HopDetail';
import type { Hop } from '../api/types';

// Charles-style detail panel: Request/Response as tabs (rather than stacked sections), plus a
// per-hop copy toolbar (curl/request/response/har) that works off the hop already in hand — no
// traceId required, unlike the pre-existing trace-level export section this sits alongside.

const HOP: Hop = {
  schema: 'ensemble/1',
  seq: 42,
  to: 'catalog',
  method: 'GET',
  path: '/widgets/1',
  status: 200,
  t: { start: '2026-01-01T00:00:00.000Z' },
  req: { headers: { 'x-req': '1' }, body: '{"in":true}' },
  resp: { headers: { 'x-resp': '1' }, body: '{"out":true}' },
};

describe('HopDetail: request/response tabs', () => {
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

  it('shows the request tab by default, hiding response', () => {
    root = createRoot(container);
    act(() => {
      root.render(createElement(HopDetail, { hop: HOP, onClose: () => {} }));
    });
    expect(container.innerHTML).toContain('x-req');
    expect(container.innerHTML).not.toContain('x-resp');
  });

  it('switches to the response tab on click, hiding request', () => {
    root = createRoot(container);
    act(() => {
      root.render(createElement(HopDetail, { hop: HOP, onClose: () => {} }));
    });
    const tab = Array.from(container.querySelectorAll('[role="tab"]')).find(
      (b) => b.textContent === 'response',
    ) as HTMLButtonElement;
    expect(tab).toBeDefined();
    act(() => {
      tab.click();
    });
    expect(container.innerHTML).toContain('x-resp');
    expect(container.innerHTML).not.toContain('x-req');
  });
});

describe('HopDetail: per-hop copy actions', () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    container = document.createElement('div');
    document.body.appendChild(container);
    Object.defineProperty(navigator, 'clipboard', {
      value: { writeText: vi.fn().mockResolvedValue(undefined) },
      configurable: true,
    });
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  it.each([
    ['copy as curl', 'curl', "curl 'http://127.0.0.1:9/widgets/1'"],
    ['copy request', 'request', 'GET /widgets/1 HTTP/1.1'],
    ['copy response', 'response', 'HTTP/1.1 200 OK'],
    ['copy as har', 'har', '{"log":{"version":"1.2"}}'],
  ])('%s fetches /api/hops/42/export?format=%s and copies the result to the clipboard', async (label, format, text) => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, text: () => Promise.resolve(text) });
    vi.stubGlobal('fetch', fetchMock);

    root = createRoot(container);
    act(() => {
      root.render(createElement(HopDetail, { hop: HOP, onClose: () => {} }));
    });

    const button = Array.from(container.querySelectorAll('button')).find((b) => b.textContent === label) as HTMLButtonElement;
    expect(button).toBeDefined();

    await act(async () => {
      button.click();
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(fetchMock).toHaveBeenCalledWith(`/api/hops/42/export?format=${format}`);
    expect(navigator.clipboard.writeText).toHaveBeenCalledWith(text);
    expect(button.textContent).toBe('copied!');
  });

  it('a clipboard failure shows "copy failed" rather than silently doing nothing', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: true, text: () => Promise.resolve('curl ...') }));
    (navigator.clipboard.writeText as ReturnType<typeof vi.fn>).mockRejectedValue(new Error('no permission'));

    root = createRoot(container);
    act(() => {
      root.render(createElement(HopDetail, { hop: HOP, onClose: () => {} }));
    });
    const button = Array.from(container.querySelectorAll('button')).find((b) => b.textContent === 'copy as curl') as HTMLButtonElement;

    await act(async () => {
      button.click();
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(button.textContent).toBe('copy failed');
  });
});
