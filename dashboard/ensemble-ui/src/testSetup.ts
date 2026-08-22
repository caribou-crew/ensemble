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
import { afterEach, beforeEach } from 'vitest';

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

const realConnect = net.Socket.prototype.connect;

beforeEach(() => {
  net.Socket.prototype.connect = function connectGuard(...args: unknown[]) {
    throw new Error(
      'ensemble-ui tests must never open a real socket — mock the api.* call that ' +
        `triggered this instead (connect() called with: ${JSON.stringify(args[0] ?? args)}).`,
    );
    // `net.Socket['connect']`'s real signature is a large overload set; a test-only guard
    // that never returns doesn't need to match it beyond "callable with anything".
  } as unknown as typeof net.Socket.prototype.connect;
});

afterEach(() => {
  net.Socket.prototype.connect = realConnect;
});
