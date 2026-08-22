import { describe, expect, it } from 'vitest';

// Re-review R-AL. testSetup.ts's socket guard has now been wrong twice, and both times the
// prose above it claimed a property the code did not have. So the three windows it says it
// watches are pinned here rather than described:
//
//   1. a connection attempt from a test file's module top level
//   2. a connection attempt from `beforeAll`
//   3. a connection attempt in the gap between one test's `afterEach` and the next test's
//      `beforeEach` (where a leaked interval fires)
//
// All three run BEFORE the `beforeEach` that used to reset the guard's counter, so all three
// were counted and then wiped before any `afterEach` could read them — the guard reported a
// clean run with real socket attempts inside it. Verified: against the pre-fix testSetup all
// three probe files below PASSED.
//
// They cannot be ordinary `*.test.ts` files, because a probe that works fails the run by
// design. They live in `src/__guardProbes/*.probe.ts` — outside this suite's `include` — and
// are executed here as a CHILD vitest process through `vitest.guard-probes.config.ts`, which
// loads the very same `src/testSetup.ts`. The assertion is on the child's result, so this
// test goes red the moment the guard stops observing any one of the three windows.

// This package has no `@types/node`; route the specifier through a non-literal `string` so
// TypeScript falls back to `Promise<any>` rather than trying to resolve a Node builtin. Same
// technique, and the same reason, as testSetup.ts's own `net` import.
const childProcessModuleName: string = 'node:child_process';
const { spawnSync } = (await import(childProcessModuleName)) as {
  spawnSync: (
    cmd: string,
    args: string[],
    opts: Record<string, unknown>,
  ) => { status: number | null; stdout: string; stderr: string };
};

const proc = (globalThis as unknown as { process: { cwd(): string } }).process;

const PROBES = ['top-level.probe.ts', 'before-all.probe.ts', 'hook-gap.probe.ts'];

describe('testSetup socket guard', () => {
  it('fails a run for an attempt made in any of the three pre-beforeEach windows', () => {
    const run = spawnSync(
      'node_modules/.bin/vitest',
      ['run', '--config', 'vitest.guard-probes.config.ts'],
      { cwd: proc.cwd(), encoding: 'utf8' },
    );
    const output = `${run.stdout}\n${run.stderr}`;

    expect(
      run.status,
      `the guard probes must FAIL the child run; they did not.\n--- child output ---\n${output}`,
    ).not.toBe(0);

    // Not just "something failed" — every one of the three windows individually.
    for (const probe of PROBES) {
      expect(
        output,
        `${probe} did not fail: the guard no longer reports an attempt from that window.\n` +
          `--- child output ---\n${output}`,
      ).toContain(probe);
    }
    expect(
      output,
      `expected all ${PROBES.length} probe files to fail.\n--- child output ---\n${output}`,
    ).toContain(`Test Files  ${PROBES.length} failed (${PROBES.length})`);
  }, 60_000);
});
