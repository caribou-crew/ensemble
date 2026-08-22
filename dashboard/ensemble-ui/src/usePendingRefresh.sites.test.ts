import { describe, expect, it } from 'vitest';

// Re-review N7, and the CLASS rather than the two instances that happen to be observable.
//
// `usePendingRefresh` is shared by three call sites, and its contract clauses — every waiter
// resolved, not just the newest (N1); settled means the load SETTLED, not that it returned a
// value (N4); a closed hook still settles (N5) — are stated once, in the hook, instead of
// three times. That is the right structure. The catch is what it does to the pinning:
// `ServicesView.useServicesPoll` and `TopologyView.useTopologyPoll` each have a behavioural
// two-waiter probe, but `TopologyView.useProfiles.reload` has none and cannot have one.
// `reload` is called only by `toggle`, and ProfileStrip sets `disabled={busy !== null}` on
// every lane for the duration, so a second concurrent waiter is not reachable through the UI
// — the code comment at that site says exactly this, and it is true.
//
// The consequence, measured rather than assumed: the re-review re-inlined the pre-N1
// single-slot resolver at `useProfiles` ALONE and the whole suite stayed green. Site three
// rode entirely on the other two sites' tests plus the hook's own unit test — which say
// nothing about whether site three still calls the hook at all.
//
// So what is pinned here is the property that makes sharing the hook load-bearing: the hook
// is the ONLY place in this package that parks a promise resolver for something else to
// settle later. A site that stops calling it has to write that machinery itself, and writing
// it is what this test forbids. Re-inline the single slot at any of the three sites and both
// halves below fail at once — the bespoke `new Promise` appears, and that file's count of
// `usePendingRefresh` calls drops.
//
// This is deliberately structural. It cannot replace the behavioural probes at sites one and
// two and does not try to: it proves the shared clauses still APPLY to a site, not that the
// site behaves correctly. Those are different claims and this package needs both.

// This package has no `@types/node`; route the specifiers through non-literal `string`s so
// TypeScript falls back to `Promise<any>` rather than resolving Node builtins. Same technique,
// and the same reason, as testSetup.ts's own `net` import.
const fsModuleName: string = 'node:fs';
const pathModuleName: string = 'node:path';
const fs = (await import(fsModuleName)) as {
  readdirSync: (
    dir: string,
    opts: { withFileTypes: true },
  ) => { name: string; isDirectory(): boolean }[];
  readFileSync: (file: string, enc: string) => string;
};
const path = (await import(pathModuleName)) as { join: (...parts: string[]) => string };
const proc = (globalThis as unknown as { process: { cwd(): string } }).process;

/** The one place allowed to build a promise it settles later. */
const HOOK = 'src/usePendingRefresh.ts';

/** Every call site, and how many awaited refreshes it owns. */
const SITES = [
  { file: 'src/views/ServicesView.tsx', calls: 1, sites: 'useServicesPoll.refresh' },
  {
    file: 'src/views/TopologyView.tsx',
    calls: 2,
    sites: 'useTopologyPoll.refresh and useProfiles.reload',
  },
];

function sourceFiles(dir: string, acc: string[] = []): string[] {
  for (const entry of fs.readdirSync(path.join(proc.cwd(), dir), { withFileTypes: true })) {
    const rel = `${dir}/${entry.name}`;
    if (entry.isDirectory()) {
      sourceFiles(rel, acc);
    } else if (/\.tsx?$/.test(entry.name) && !/\.(test|probe)\.tsx?$/.test(entry.name)) {
      acc.push(rel);
    }
  }
  return acc;
}

function read(file: string): string {
  return fs.readFileSync(path.join(proc.cwd(), file), 'utf8');
}

describe('usePendingRefresh is the only deferred-resolver in the package', () => {
  it('builds a promise for something else to settle nowhere outside the hook', () => {
    const files = sourceFiles('src');
    const offenders: string[] = [];

    for (const file of files) {
      if (file === HOOK) continue;
      read(file)
        .split('\n')
        .forEach((line, i) => {
          // Comments discuss promises freely — this is about code.
          const code = line.replace(/\/\/.*$/, '');
          if (/new Promise\b/.test(code)) offenders.push(`${file}:${i + 1}  ${line.trim()}`);
        });
    }

    expect(
      files.length,
      'the scan found no source files — the walk is broken, not the invariant satisfied',
    ).toBeGreaterThan(15);

    expect(
      offenders,
      'a hand-rolled resolver is how the N1 hang (a single slot, silently overwritten), the ' +
        'N4 hang (draining on the value rather than on the load settling) and the N5 hang (a ' +
        'resolver parked after unmount) each got written in the first place. Every one of ' +
        'them is fixed ONCE, in usePendingRefresh, and a site that rebuilds the machinery ' +
        'locally opts out of all three fixes silently. Route it through the hook instead; if ' +
        'a deferred promise here is genuinely something else, add it to this test with a ' +
        'reason rather than deleting the assertion:\n' +
        offenders.join('\n'),
    ).toEqual([]);
  });

  it('is still what every one of the three call sites obtains its refresh from', () => {
    for (const site of SITES) {
      const count = read(site.file).split('usePendingRefresh(').length - 1;
      expect(
        count,
        `${site.file} should get ${site.calls} awaited refresh(es) from the shared hook ` +
          `(${site.sites}) and gets ${count}. A site that stopped calling it no longer ` +
          'inherits the all-waiters-resolved, settle-on-loading and settle-when-closed ' +
          'clauses — and at useProfiles, where a second concurrent waiter is not reachable ' +
          'through the UI, nothing behavioural would notice (re-review N7).',
      ).toBe(site.calls);
    }
  });
});
