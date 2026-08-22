// Makes one real `net.Socket#connect` call — the exact call the suite-wide guard in
// src/testSetup.ts patches — and SWALLOWS the guard's throw, the way a component that
// starts an unmocked `api.*` call in an effect swallows it as a dropped promise rejection.
// Swallowing is the whole point: it is what makes the throw invisible and the counted
// attempt the only evidence left (final review F4).
//
// No connection is ever opened: the guard replaces `connect` and throws before any syscall.
// The ports below are 1/2/3 regardless, so that a probe run against an UNGUARDED process
// could still never reach anything real.
const nodeNetModuleName: string = 'net';
const net = (await import(nodeNetModuleName)) as {
  connect: (port: number, host: string) => unknown;
};

/**
 * One DISTINCT unreachable port per window, and that distinctness is load-bearing rather
 * than decorative.
 *
 * The guard's `afterEach` reports the attempt target it recorded, so with a shared port all
 * three windows produced the byte-identical `AssertionError` and vitest collapsed them into
 * ONE deduplicated error block (`⎯[1/3]⎯`). `testSetup.guard.test.ts` reads that output and
 * has to attribute a guard report to each window separately — otherwise it is back to
 * asserting "three files failed", which is exactly the vacuity re-review N6 caught: with
 * `attemptConnection` throwing an unrelated error and touching no socket at all, the child
 * still reported `Test Files  3 failed (3)` and the parent suite stayed green.
 *
 * A port per window makes the three messages distinct, so vitest prints all three and the
 * parent can require the guard's own phrase alongside each window's own target.
 */
export const PROBE_WINDOWS = {
  'top-level': 1,
  'before-all': 2,
  'hook-gap': 3,
} as const;

export type ProbeWindow = keyof typeof PROBE_WINDOWS;

export function attemptConnection(where: ProbeWindow): void {
  try {
    net.connect(PROBE_WINDOWS[where], '127.0.0.1');
  } catch {
    // swallowed on purpose — see above
  }
}
