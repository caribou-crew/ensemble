// Suite-wide guard: ensemble-ui's tests talk to the ensemble API exclusively through
// `vi.spyOn(api, ...)` mocks — nothing here should ever open a real TCP connection. Before
// this existed, TopologyView's two race tests left `api.profiles()` unmocked, and its 5s
// poll (`useProfiles`) quietly attempted a real connection to 127.0.0.1:3000 on every test
// run, succeeding or failing (ECONNREFUSED) depending on what else happened to be listening
// there — see F.18. The mocks are the actual fix; this is what keeps that fix from silently
// rotting the next time a view grows a new unmocked call site.
//
// Patching `net.Socket.prototype.connect` catches every path a real network attempt can take
// (`net.connect`, `net.createConnection`, and Node's built-in `fetch`/undici, which all funnel
// through a `Socket` instance's own `connect()`), without needing to know which API a given
// test's code path happens to use.
//
// DESIGN, REVISED (final review F4): the original version threw straight from `connect()`,
// installed/removed per-test in `beforeEach`/`afterEach`. That throw only fails a test when
// something on the call stack propagates it — a component that starts an unmocked `api.*`
// call in an effect and swallows the rejection (`useProfiles`'s load, at the time of the
// review) turns the throw into a silently-dropped promise rejection, and the run reports
// success with a real connection attempt inside it. The review's own probe proved this:
// rendering `TopologyView` with `api.profiles` unmocked printed `GUARD FIRED 1 TIME(S) — and
// this test still passed`. A thrown error is not a substitute for an assertion; only an
// assertion fails the test regardless of what happens to the promise it came from.
//
// So the guard now counts attempts instead of relying on the throw being observed, and
// installs itself ONCE at module load rather than per-test. A per-test install/teardown left
// three windows completely UNWATCHED: a test file's own top-level code, any `beforeAll` hook
// (both run before the first `beforeEach`), and the gap between one test's `afterEach` and
// the next test's `beforeEach`, where a leaked interval fires. Installing once, for the whole
// file's lifetime, is what makes an attempt from those windows get COUNTED.
//
// Counting it is not the same as REPORTING it, and an earlier version of this comment
// claimed installing once "closes all three" holes when it closed only the installation half
// (re-review R-AL). The reporting half was defeated by a `beforeEach` that reset the counter:
// `beforeEach` runs AFTER module top-level code and AFTER `beforeAll`, so an attempt from
// either window was counted and then wiped before any `afterEach` could read it — and an
// attempt in the hook gap was wiped by the NEXT test's `beforeEach` before that test's
// `afterEach` ran. That reset is gone. `afterEach` below both reads AND resets, which is all
// the per-test isolation this ever needed; resetting on the way in only ever discarded
// evidence gathered on the way there.
//
// All three windows are pinned, not asserted in prose: `src/__guardProbes/*.probe.ts` makes a
// real swallowed attempt from each one, and `src/testSetup.guard.test.ts` runs them as a
// child vitest process and fails if any of them is not caught. Reinstate a `beforeEach` reset
// and that test goes red.
import { afterEach, expect } from 'vitest';

// This package has no `@types/node` dependency (nothing else here runs outside the browser),
// and TypeScript refuses a `declare module` augmentation for a name it already recognizes as
// a Node builtin ('net'/'node:net') without one installed — pulling in the dependency (and
// the lockfile churn with it) for one test-only file isn't worth it. Routing the specifier
// through a non-literal `string` keeps TS from trying to resolve it at all: a dynamic
// `import()` whose argument isn't a string-literal type falls back to `Promise<any>`.
const nodeNetModuleName: string = 'net';
const net = (await import(nodeNetModuleName)) as {
  Socket: { prototype: { connect: (...args: unknown[]) => unknown } };
};

let connectAttempts = 0;
let lastAttempt: unknown;

// Installed once, at import time — before any `beforeAll`/`beforeEach` in the file that
// pulled this in via `setupFiles`, and never torn down between tests, so nothing can run in
// an unguarded gap for the lifetime of the process running this file's tests.
net.Socket.prototype.connect = function connectGuard(...args: unknown[]) {
  connectAttempts += 1;
  lastAttempt = args[0] ?? args;
  throw new Error(
    'ensemble-ui tests must never open a real socket — mock the api.* call that ' +
      `triggered this instead (connect() called with: ${JSON.stringify(lastAttempt)}).`,
  );
  // `net.Socket['connect']`'s real signature is a large overload set; a test-only guard
  // that never returns doesn't need to match it beyond "callable with anything".
} as unknown as typeof net.Socket.prototype.connect;

afterEach(() => {
  // The load-bearing check: an `expect` failure here fails the test even when the guard's
  // own thrown error above never propagated anywhere a human (or a `× ...` line) would see
  // it — e.g. because it became a promise rejection some component's own `catch` (or
  // `useAsync`'s) absorbed. Counting attempts and asserting zero AFTER the test body has
  // finished, rather than trusting the throw to be observed, is what makes that case fail a
  // run instead of passing it silently.
  const count = connectAttempts;
  const attempt = lastAttempt;
  connectAttempts = 0;
  lastAttempt = undefined;
  expect(
    count,
    count > 0
      ? `this test made ${count} real socket connection attempt(s) — mock the api.* call ` +
          `responsible (last attempt target: ${JSON.stringify(attempt)})`
      : undefined,
  ).toBe(0);
});
