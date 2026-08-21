import { act } from 'react';
import { createElement, type ReactNode } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { useAsync, type AsyncState } from './useAsync';

/**
 * Clause 4 ("after unmount, nothing is set") is NOT observable through
 * rendering: React 19 silently discards an update to an unmounted fiber, so a
 * test that only counts renders passes whether or not the hook guards, and
 * deleting the guard survives it. Wrapping `useState` lets the test observe
 * the setter call itself, which is the thing clause 4 forbids. Everything
 * else is forwarded to the real React, so the wrapper is transparent to the
 * other five tests.
 */
const { setStateCalls } = vi.hoisted(() => ({ setStateCalls: [] as unknown[] }));

vi.mock('react', async () => {
  const actual = await vi.importActual<typeof import('react')>('react');
  return {
    ...actual,
    useState: (init: unknown) => {
      const [value, set] = actual.useState(init as never);
      return [
        value,
        (next: unknown) => {
          setStateCalls.push(next);
          return (set as (v: unknown) => void)(next);
        },
      ];
    },
  };
});

let container: HTMLDivElement;
let root: Root;

beforeEach(() => {
  setStateCalls.length = 0;
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
});
afterEach(() => {
  act(() => root.unmount());
  container.remove();
});

/** Renders useAsync and exposes every state it has been through. */
function renderHook<T>(fn: () => Promise<T>, deps: readonly unknown[]) {
  const states: AsyncState<T>[] = [];
  function Probe({ f, d }: { f: () => Promise<T>; d: readonly unknown[] }): ReactNode {
    states.push(useAsync(f, d));
    return null;
  }
  const render = (f: () => Promise<T>, d: readonly unknown[]) =>
    act(() => root.render(createElement(Probe, { f, d })));
  render(fn, deps);
  return { states, render, last: () => states[states.length - 1] };
}

/** A promise whose settlement this test controls. */
function deferred<T>() {
  let resolve!: (v: T) => void;
  let reject!: (e: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  // Nothing here ever rejects unobserved; the tests always attach the hook
  // first. This keeps an unhandled-rejection warning from masking a failure.
  promise.catch(() => {});
  return { promise, resolve, reject };
}

describe('useAsync', () => {
  it('reports loading synchronously, then the resolved data', async () => {
    const d = deferred<string>();
    const h = renderHook(() => d.promise, ['a']);
    expect(h.last()).toEqual({ data: null, error: null, loading: true });
    await act(async () => {
      d.resolve('hello');
    });
    expect(h.last()).toEqual({ data: 'hello', error: null, loading: false });
  });

  it('reports a rejection as an Error and never as data', async () => {
    const d = deferred<string>();
    const h = renderHook(() => d.promise, ['a']);
    await act(async () => {
      d.reject(new Error('boom'));
    });
    expect(h.last().data).toBeNull();
    expect(h.last().error?.message).toBe('boom');
    expect(h.last().loading).toBe(false);
  });

  it('wraps a non-Error rejection so consumers can always read .message', async () => {
    const d = deferred<string>();
    const h = renderHook(() => d.promise, ['a']);
    await act(async () => {
      d.reject('just a string');
    });
    expect(h.last().error).toBeInstanceOf(Error);
    expect(h.last().error?.message).toBe('just a string');
  });

  it('clears stale data the instant deps change, before the new load settles', async () => {
    const first = deferred<string>();
    const h = renderHook(() => first.promise, ['a']);
    await act(async () => {
      first.resolve('page A');
    });
    expect(h.last().data).toBe('page A');

    // "Synchronously" in clause 1 means synchronously: this flag flips on the
    // first microtask turn, so asserting it is still false proves the cleared
    // state was rendered without the test ever yielding to the microtask queue.
    let yielded = false;
    queueMicrotask(() => {
      yielded = true;
    });

    const second = deferred<string>();
    h.render(() => second.promise, ['b']);
    // This is the EntityDetail bug: the previous entity's body rendered
    // under the new entity's heading until the new fetch landed.
    expect(h.last()).toEqual({ data: null, error: null, loading: true });
    expect(yielded).toBe(false);

    await act(async () => {
      second.resolve('page B');
    });
    expect(h.last().data).toBe('page B');
  });

  it('discards a slow earlier load that settles after a newer one', async () => {
    const slow = deferred<string>();
    const h = renderHook(() => slow.promise, ['a']);
    const fast = deferred<string>();
    h.render(() => fast.promise, ['b']);

    await act(async () => {
      fast.resolve('newest');
    });
    await act(async () => {
      slow.resolve('stale');
    }); // arrives last, must lose
    expect(h.last()).toEqual({ data: 'newest', error: null, loading: false });
  });

  it('discards a slow earlier load that REJECTS after a newer one resolved', async () => {
    // The other arm of the same guard. Without this the rejection handler's
    // generation check can be deleted with the suite still green, and a stale
    // 404 from the previous deps would blank a page that had already loaded.
    const slow = deferred<string>();
    const h = renderHook(() => slow.promise, ['a']);
    const fast = deferred<string>();
    h.render(() => fast.promise, ['b']);

    await act(async () => {
      fast.resolve('newest');
    });
    await act(async () => {
      slow.reject(new Error('stale failure'));
    });
    expect(h.last()).toEqual({ data: 'newest', error: null, loading: false });
  });

  it('sets nothing after unmount', async () => {
    const d = deferred<string>();
    const h = renderHook(() => d.promise, ['a']);
    const before = h.states.length;
    act(() => root.unmount());
    const setsBefore = setStateCalls.length;
    await act(async () => {
      d.resolve('too late');
    });
    expect(h.states.length).toBe(before);
    // React would swallow a post-unmount update silently, so the render count
    // above cannot see the defect on its own. The setter call can.
    expect(setStateCalls.length).toBe(setsBefore);
  });

  it('sets nothing after unmount when the in-flight load rejects', async () => {
    const d = deferred<string>();
    renderHook(() => d.promise, ['a']);
    act(() => root.unmount());
    const setsBefore = setStateCalls.length;
    await act(async () => {
      d.reject(new Error('too late'));
    });
    expect(setStateCalls.length).toBe(setsBefore);
  });
});
