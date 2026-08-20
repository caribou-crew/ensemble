import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import LatencyView from './LatencyView';

// A fetch-shaped stand-in (ok/status/statusText/text()) rather than a real
// Response — client.ts's request() only ever touches those four members,
// and happy-dom's Response/fetch polyfills are not otherwise exercised
// here since the global `fetch` itself is replaced wholesale.
function fakeResponse(body: unknown, status = 200) {
  return {
    ok: status >= 200 && status < 300,
    status,
    statusText: status === 200 ? 'OK' : 'Error',
    text: () => Promise.resolve(JSON.stringify(body)),
  };
}

/** Flushes N extra microtask turns inside act() — the round trip under test
 * runs fetch() -> res.text() -> JSON.parse -> .then() -> setState, more
 * hops than a single act() callback reliably drains on its own. */
async function flush(turns = 8) {
  for (let i = 0; i < turns; i++) {
    // eslint-disable-next-line no-await-in-loop
    await act(async () => {
      await Promise.resolve();
    });
  }
}

describe('LatencyView: rule edit round-trip', () => {
  let container: HTMLDivElement;
  let root: Root;
  let fetchMock: ReturnType<typeof vi.fn>;

  const initialRules = [{ target: 'svc-a', path: '/x', fixedMs: 100, enabled: true }];
  const updatedRules = [{ target: 'svc-a', path: '/x', fixedMs: 250, enabled: true }];

  beforeEach(() => {
    container = document.createElement('div');
    document.body.appendChild(container);

    fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === 'string' ? input : input.toString();
      if (url === '/api/latency' && init === undefined) {
        return Promise.resolve(fakeResponse({ rules: initialRules }));
      }
      if (url === '/api/latency' && init?.method === 'PUT') {
        return Promise.resolve(fakeResponse({ rules: updatedRules }));
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

  it('edits a rule inline and round-trips the server response', async () => {
    root = createRoot(container);
    await act(async () => {
      root.render(createElement(LatencyView));
    });
    await flush();

    expect(container.textContent).toContain('100ms fixed');

    const editButton = Array.from(container.querySelectorAll('button')).find((b) => b.textContent === 'edit');
    expect(editButton).toBeTruthy();
    await act(async () => {
      editButton!.click();
    });

    const fixedInput = container.querySelector('input[name="fixedMs"]') as HTMLInputElement | null;
    expect(fixedInput).toBeTruthy();
    // React tracks the input's value on the instance itself, so a plain
    // `input.value = ...` assignment is invisible to its change-detection —
    // the standard workaround is going through the native prototype's
    // setter directly before dispatching the event.
    const nativeSetter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value')!.set!;
    await act(async () => {
      nativeSetter.call(fixedInput, '250');
      fixedInput!.dispatchEvent(new Event('input', { bubbles: true }));
    });
    expect(fixedInput!.value).toBe('250');

    const saveButton = Array.from(container.querySelectorAll('button')).find((b) => b.textContent === 'save');
    expect(saveButton).toBeTruthy();
    await act(async () => {
      saveButton!.click();
    });
    await flush();

    const putCall = fetchMock.mock.calls.find(([, init]) => (init as RequestInit | undefined)?.method === 'PUT');
    expect(putCall).toBeTruthy();
    const sentBody = JSON.parse((putCall![1] as RequestInit).body as string);
    expect(sentBody).toMatchObject({ target: 'svc-a', path: '/x', fixedMs: 250, enabled: true });

    // The form closes and the row now reflects what the SERVER returned
    // (updatedRules), not just the locally-typed value — the actual
    // round trip, not an optimistic local echo.
    expect(container.querySelector('input[name="fixedMs"]')).toBeNull();
    expect(container.textContent).toContain('250ms fixed');
    expect(container.textContent).not.toContain('100ms fixed');
  });
});
