import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import LatencyView from './LatencyView';

// Final review F8 (= F.19): LatencyView's `deps: []` deviation (see LatencyView.tsx's own
// comment) is safe only as long as NOTHING ever re-triggers the initial GET /api/latency
// load — the reviewer's mutation M10 (the brief's prescribed version-counter refetch) is
// caught by LatencyView.test.tsx's round-trip assertion, but M11 (the same version counter
// bumped from `toggleEnabled` instead, a handler that test never clicks) typechecks clean
// and leaves the whole suite green, so the premise rested on a comment, not a guard. This
// test closes that gap directly: it counts GET /api/latency calls and asserts there is
// still exactly one after both a toggle AND a delete, so ANY future change that adds a
// second load — whichever handler it's wired to — fails this test, not just the one variant
// a reviewer happened to think of.

function fakeResponse(body: unknown, status = 200) {
  return {
    ok: status >= 200 && status < 300,
    status,
    statusText: status === 200 ? 'OK' : 'Error',
    text: () => Promise.resolve(JSON.stringify(body)),
  };
}

async function flush(turns = 8) {
  for (let i = 0; i < turns; i++) {
    // eslint-disable-next-line no-await-in-loop
    await act(async () => {
      await Promise.resolve();
    });
  }
}

describe('LatencyView: deps: [] never re-fires the GET load', () => {
  let container: HTMLDivElement;
  let root: Root;
  let fetchMock: ReturnType<typeof vi.fn>;
  let getCalls: number;

  const initialRules = [
    { target: 'svc-a', path: '/x', fixedMs: 100, enabled: true },
    { target: 'svc-b', path: '/y', fixedMs: 200, enabled: true },
  ];
  const toggledRules = [
    { target: 'svc-a', path: '/x', fixedMs: 100, enabled: false },
    { target: 'svc-b', path: '/y', fixedMs: 200, enabled: true },
  ];
  const afterDeleteRules = [{ target: 'svc-a', path: '/x', fixedMs: 100, enabled: false }];

  beforeEach(() => {
    container = document.createElement('div');
    document.body.appendChild(container);
    getCalls = 0;
    window.confirm = () => true;

    fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === 'string' ? input : input.toString();
      if (url === '/api/latency' && init === undefined) {
        getCalls += 1;
        return Promise.resolve(fakeResponse({ rules: initialRules }));
      }
      if (url === '/api/latency' && init?.method === 'PUT') {
        return Promise.resolve(fakeResponse({ rules: toggledRules }));
      }
      if (url.startsWith('/api/latency?') && init?.method === 'DELETE') {
        return Promise.resolve(fakeResponse({ rules: afterDeleteRules }));
      }
      return Promise.reject(new Error(`unexpected fetch ${init?.method ?? 'GET'} ${url}`));
    });
    vi.stubGlobal('fetch', fetchMock);
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it('stays at exactly one GET /api/latency across a toggle AND a delete', async () => {
    root = createRoot(container);
    await act(async () => {
      root.render(createElement(LatencyView));
    });
    await flush();

    expect(getCalls, 'the initial mount load').toBe(1);

    const toggleButton = container.querySelector('.latency-table__toggle') as HTMLButtonElement | null;
    expect(toggleButton, 'expected a toggle button on the first row').toBeTruthy();
    await act(async () => {
      toggleButton!.click();
    });
    await flush();
    expect(container.textContent).toContain('disarmed');
    expect(getCalls, 'a toggle must update `rules` from its own PUT response, not a reload').toBe(1);

    const deleteButtons = Array.from(container.querySelectorAll('button')).filter((b) => b.textContent === 'delete');
    expect(deleteButtons.length, 'expected a delete button per row').toBeGreaterThan(0);
    await act(async () => {
      deleteButtons[deleteButtons.length - 1]!.click();
    });
    await flush();
    expect(getCalls, 'a delete must also update `rules` from its own response, not a reload').toBe(1);
  });
});
