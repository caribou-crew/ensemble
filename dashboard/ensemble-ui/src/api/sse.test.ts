import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { subscribeHops } from './sse';
import type { Hop } from './types';

// Stubs the global EventSource the way a real browser would deliver it:
// addEventListener('hop', ...) frames plus an assignable onerror. Every
// constructed instance is recorded so a test can inspect the URL each
// reconnect actually opened.
class FakeEventSource {
  static instances: FakeEventSource[] = [];
  url: string;
  closed = false;
  onerror: (() => void) | null = null;
  private listeners = new Map<string, Array<(evt: MessageEvent<string>) => void>>();

  constructor(url: string) {
    this.url = url;
    FakeEventSource.instances.push(this);
  }

  addEventListener(type: string, cb: (evt: MessageEvent<string>) => void) {
    const list = this.listeners.get(type) ?? [];
    list.push(cb);
    this.listeners.set(type, list);
  }

  close() {
    this.closed = true;
  }

  emitHop(hop: Hop) {
    const data = JSON.stringify(hop);
    for (const cb of this.listeners.get('hop') ?? []) {
      cb({ data } as MessageEvent<string>);
    }
  }

  triggerError() {
    this.onerror?.();
  }
}

function hop(seq: number): Hop {
  return { schema: 'ensemble/1', seq, to: 'svc', t: { start: '2026-08-20T00:00:00.000Z' } };
}

describe('subscribeHops', () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal('EventSource', FakeEventSource);
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it('delivers parsed hops in order', () => {
    const received: Hop[] = [];
    const unsubscribe = subscribeHops(0, (h) => received.push(h));

    const es = FakeEventSource.instances[0];
    es.emitHop(hop(1));
    es.emitHop(hop(2));
    es.emitHop(hop(3));

    expect(received.map((h) => h.seq)).toEqual([1, 2, 3]);
    unsubscribe();
  });

  it('opens the initial connection with the given since cursor', () => {
    const unsubscribe = subscribeHops(42, () => {});
    expect(FakeEventSource.instances[0].url).toBe('/api/traffic/stream?since=42');
    unsubscribe();
  });

  it('reconnects after an error, 1s later, carrying the last-seen seq as since', () => {
    const unsubscribe = subscribeHops(0, () => {});
    const first = FakeEventSource.instances[0];
    first.emitHop(hop(7));
    first.emitHop(hop(9));

    first.triggerError();
    // No new connection until the backoff elapses.
    expect(FakeEventSource.instances.length).toBe(1);
    expect(first.closed).toBe(true);

    vi.advanceTimersByTime(1000);

    expect(FakeEventSource.instances.length).toBe(2);
    expect(FakeEventSource.instances[1].url).toBe('/api/traffic/stream?since=9');
    unsubscribe();
  });

  it('a hop delivered on the reconnected source still reaches onHop', () => {
    const received: Hop[] = [];
    const unsubscribe = subscribeHops(0, (h) => received.push(h));
    const first = FakeEventSource.instances[0];
    first.emitHop(hop(1));
    first.triggerError();
    vi.advanceTimersByTime(1000);

    const second = FakeEventSource.instances[1];
    second.emitHop(hop(2));

    expect(received.map((h) => h.seq)).toEqual([1, 2]);
    unsubscribe();
  });

  it('unsubscribe closes the current connection', () => {
    const unsubscribe = subscribeHops(0, () => {});
    const es = FakeEventSource.instances[0];
    unsubscribe();
    expect(es.closed).toBe(true);
  });

  it('unsubscribe during the reconnect backoff cancels the pending reconnect', () => {
    const unsubscribe = subscribeHops(0, () => {});
    const first = FakeEventSource.instances[0];
    first.triggerError();
    unsubscribe();

    vi.advanceTimersByTime(5000);
    expect(FakeEventSource.instances.length).toBe(1);
  });
});
