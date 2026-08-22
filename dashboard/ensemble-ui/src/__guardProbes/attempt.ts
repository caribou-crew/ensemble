// Makes one real `net.Socket#connect` call — the exact call the suite-wide guard in
// src/testSetup.ts patches — and SWALLOWS the guard's throw, the way a component that
// starts an unmocked `api.*` call in an effect swallows it as a dropped promise rejection.
// Swallowing is the whole point: it is what makes the throw invisible and the counted
// attempt the only evidence left (final review F4).
//
// No connection is ever opened: the guard replaces `connect` and throws before any syscall.
// The port is 1 regardless, so that a probe run against an UNGUARDED process could still
// never reach anything real.
const nodeNetModuleName: string = 'net';
const net = (await import(nodeNetModuleName)) as {
  connect: (port: number, host: string) => unknown;
};

export function attemptConnection(where: string): void {
  try {
    net.connect(1, '127.0.0.1');
  } catch {
    // swallowed on purpose — see above
    void where;
  }
}
