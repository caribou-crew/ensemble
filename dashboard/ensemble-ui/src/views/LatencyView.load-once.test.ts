import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import LatencyView from './LatencyView';
import { api } from '../api/client';
import type { LatencyRule } from '../api/types';

// Final review F8 (= F.19), REOPENED by the re-review and closed here. LatencyView's
// `deps: []` deviation (see LatencyView.tsx's own comment) is safe only as long as NOTHING
// ever re-triggers the initial GET /api/latency load. The deviation itself stands; what has
// to be true is that its stated premise cannot rot into a silent pass.
//
// The first attempt at this file SAMPLED HANDLERS: it clicked a toggle and a delete and
// counted GETs. That catches a version counter bumped from `toggleEnabled`, and nothing
// else — the re-review demonstrated that a counter bumped from `resetAll`, and the brief's
// own named scenario of an actual `refresh` button, both left all 115 tests green. Sampling
// two of six handlers is not the same as guarding the property, and the comment that used to
// sit here claimed otherwise ("whichever handler it's wired to"), which was the F11 defect
// all over again.
//
// So the property is asserted directly, two ways, neither of which depends on a test
// remembering to click the right control:
//
//   1. `the load's deps array is empty` — the deps LatencyView hands `useAsync` are captured
//      on every render. A re-fire has to come from a dep, so this fails for a version
//      counter no matter which handler bumps it, or whether any handler is wired to it yet.
//   2. `no control re-fires the load` — every button, input and select the view renders is
//      exercised, discovered from the DOM rather than named here, and the GET count must
//      still be 1. A control added tomorrow is swept tomorrow, with no edit to this file.
//
// WHAT THIS STILL DOES NOT CATCH, stated plainly rather than implied away: a handler that
// calls `api.latencyList()` directly from a code path no rendered control reaches and no dep
// records — an SSE callback, say, or a `setTimeout` longer than the sweep's timer advance.
// Check 2 covers the reachable-by-a-control half of that; check 1 covers everything that
// goes through `useAsync`. Between them the two named F8 mutations and the brief's refresh
// button all fail; a hand-rolled fetch from an event source would not.

// Captures the deps array LatencyView hands `useAsync`, on every render, without changing
// what the hook does — the wrapper delegates straight to the real implementation. `vi.mock`
// is hoisted above the imports above, so the shared box has to be created with `vi.hoisted`.
const captured = vi.hoisted(() => ({ deps: [] as (readonly unknown[])[] }));

vi.mock('@ensemble/design-system/useAsync', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@ensemble/design-system/useAsync')>();
  return {
    ...actual,
    useAsync: (fn: () => Promise<unknown>, deps: readonly unknown[]) => {
      captured.deps.push(deps);
      return actual.useAsync(fn, deps);
    },
  };
});

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

describe('LatencyView: the one-shot load is a property, not a sampled handler', () => {
  let container: HTMLDivElement;
  let root: Root;

  const RULES: LatencyRule[] = [
    { target: 'svc-a', path: '/x', fixedMs: 100, enabled: true },
    { target: 'svc-b', path: '/y', fixedMs: 200, enabled: true },
  ];

  beforeEach(() => {
    vi.useFakeTimers();
    captured.deps.length = 0;
    container = document.createElement('div');
    document.body.appendChild(container);
    window.confirm = () => true;
    // Every latency endpoint the view can reach is mocked, so the sweep below can press any
    // control it finds without a rejected fetch replacing the table and cutting it short.
    vi.spyOn(api, 'latencyList').mockResolvedValue(RULES);
    vi.spyOn(api, 'latencyUpsert').mockResolvedValue(RULES);
    vi.spyOn(api, 'latencyDelete').mockResolvedValue(RULES);
    vi.spyOn(api, 'latencyArmAll').mockResolvedValue(RULES);
    vi.spyOn(api, 'latencyReset').mockResolvedValue(RULES);
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    vi.restoreAllMocks();
    vi.useRealTimers();
  });

  async function mount() {
    root = createRoot(container);
    await act(async () => {
      root.render(createElement(LatencyView));
    });
    await flush();
  }

  it('hands useAsync an EMPTY deps array, so no handler can make the load re-fire', async () => {
    await mount();

    expect(captured.deps.length, 'LatencyView must have called useAsync at least once').toBeGreaterThan(0);
    // One call site, one contract. Anything a future edit could bump — a version counter, a
    // refreshToken, an SSE cursor — shows up here as a non-empty deps array regardless of
    // which handler (if any) is wired to bump it, which is exactly what "sampling handlers"
    // could not see.
    for (const deps of captured.deps) {
      expect(
        deps.length,
        `LatencyView's useAsync deps must stay empty — got [${deps.map((d) => String(d)).join(', ')}]. ` +
          'A non-empty deps array means the GET /api/latency load can re-fire, which the ' +
          "view's own deviation comment states it never does; either that premise or this " +
          'assertion has to change, deliberately and together.',
      ).toBe(0);
    }
    expect(
      new Set(captured.deps).size >= 1 && captured.deps.every((d) => d.length === 0),
      'every render must pass the same empty deps',
    ).toBe(true);
  });

  it('re-fires the load for NO control the view renders, including ones added later', async () => {
    await mount();
    expect(api.latencyList).toHaveBeenCalledTimes(1);

    // Controls are discovered from the DOM, not named here, so a `refresh` button added
    // tomorrow is pressed by this test tomorrow without anyone remembering to add it.
    const pressed = new Set<string>();
    const signature = (el: Element) =>
      `${el.tagName}|${el.className}|${(el as HTMLInputElement).type ?? ''}|` +
      `${(el as HTMLInputElement).name ?? ''}|${el.getAttribute('placeholder') ?? ''}|${el.textContent ?? ''}`;

    // Bounded so a view that regenerates controls forever cannot hang the run; the
    // assertion after the loop reports if the bound was actually hit.
    const LIMIT = 60;
    let acted = 0;
    for (let step = 0; step < LIMIT; step += 1) {
      const next = Array.from(
        container.querySelectorAll<HTMLElement>('button, input, select, textarea'),
      ).find((el) => !pressed.has(signature(el)));
      if (!next) break;
      pressed.add(signature(next));
      acted += 1;
      await act(async () => {
        if (next instanceof HTMLInputElement && (next.type === 'checkbox' || next.type === 'radio')) {
          next.click();
        } else if (next instanceof HTMLInputElement || next instanceof HTMLTextAreaElement) {
          next.value = '7';
          next.dispatchEvent(new Event('input', { bubbles: true }));
        } else if (next instanceof HTMLSelectElement) {
          next.dispatchEvent(new Event('change', { bubbles: true }));
        } else {
          next.click();
        }
      });
      await flush();
    }
    expect(acted, 'the sweep must have found controls to exercise').toBeGreaterThan(3);
    expect(acted, `the control sweep hit its ${LIMIT}-step bound; raise it or narrow the view`).toBeLessThan(LIMIT);

    // And nothing on a timer either.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(60_000);
    });
    await flush();

    expect(
      api.latencyList,
      `something re-fired GET /api/latency after exercising ${acted} control(s) and 60s of timers — ` +
        "LatencyView's deps: [] deviation is only safe while this stays at 1",
    ).toHaveBeenCalledTimes(1);
  });
});
