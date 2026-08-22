import { describe, expect, it } from 'vitest';
import { PROBE_WINDOWS } from './__guardProbes/attempt';

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
//
// WHY THE ASSERTIONS LOOK AT THE GUARD'S OWN MESSAGE AND NOT JUST AT "IT FAILED"
// (re-review N6). The earlier version of this test asserted only: non-zero exit, each probe
// FILENAME present, and `Test Files  3 failed (3)`. Every one of those is satisfied by any
// failure for any reason. Proven, not supposed: with `attemptConnection` rewritten to touch
// no socket at all and throw an unrelated error, the child still reported
// `Test Files  3 failed (3)` and this suite stayed green — so the mechanism whose entire
// subject is "a probe that silently no-ops proves nothing" was itself satisfied by three
// probes that proved nothing. That is the same defect class as F4 and R-AL, recurring inside
// its own fix for the third time in this task, which is why the check below is on the
// guard's own words: `real socket connection attempt(s)`, reported against each window's own
// connect target. A probe that stops touching a socket, or a guard that stops noticing,
// takes that phrase out of the child's output and this test goes red.
//
// (The opposite direction was already safe: a probe that no-ops WITHOUT failing makes the
// child exit 0, which the status assertion catches.)

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

const proc = (globalThis as unknown as {
  process: { cwd(): string; env: Record<string, string | undefined> };
}).process;

/** The phrase testSetup.ts's `afterEach` assertion puts in front of a human. */
const GUARD_REPORT = 'real socket connection attempt';

// The child's output is matched as text, so it must be PLAIN text. On a developer machine it
// is: vitest sees a non-TTY pipe and emits no colour, which is why every assertion below
// passed locally. On GitHub Actions it is not — vitest turns on its `github-actions` reporter
// and colourises, so the summary line arrives as
// `\x1b[2m Test Files \x1b[22m \x1b[1m\x1b[31m3 failed\x1b[39m...` and a literal
// `Test Files  3 failed (3)` cannot match. That is exactly what happened: this suite was
// green locally and red in CI for a reason that had nothing to do with the guard it tests.
//
// Belt AND braces, deliberately. The env vars ask the child not to colourise; `stripAnsi`
// makes the assertions hold even if something colourises anyway (a future vitest, a reporter
// that ignores NO_COLOR, a CI provider with its own opinion). Relying on the env alone would
// leave this test's correctness depending on a setting in someone else's tool.
const stripAnsi = (s: string): string =>
  // eslint-disable-next-line no-control-regex -- matching ANSI SGR escapes is the point
  s.replace(/\x1b\[[0-9;]*m/g, '');

// filename ↔ the port that file's probe connects to, so a guard report can be attributed to
// one window rather than to "somewhere in the child run". The ports come from the probes'
// own table, not a copy of it.
const PROBES = [
  { file: 'top-level.probe.ts', window: 'top-level' as const },
  { file: 'before-all.probe.ts', window: 'before-all' as const },
  { file: 'hook-gap.probe.ts', window: 'hook-gap' as const },
];

describe('testSetup socket guard', () => {
  it('reports a real socket attempt from each of the three pre-beforeEach windows', () => {
    const run = spawnSync(
      'node_modules/.bin/vitest',
      ['run', '--config', 'vitest.guard-probes.config.ts'],
      {
        cwd: proc.cwd(),
        encoding: 'utf8',
        // NO_COLOR is the cross-tool convention; FORCE_COLOR=0 covers anything that only
        // honours the node/chalk one. GITHUB_ACTIONS is cleared because vitest switches on its
        // `github-actions` reporter when it sees that variable, which is what colourised this
        // child's output on the runner in the first place.
        env: { ...proc.env, NO_COLOR: '1', FORCE_COLOR: '0', GITHUB_ACTIONS: '' },
      },
    );
    const output = stripAnsi(`${run.stdout}\n${run.stderr}`);

    expect(
      run.status,
      `the guard probes must FAIL the child run; they did not.\n--- child output ---\n${output}`,
    ).not.toBe(0);

    for (const probe of PROBES) {
      // Not just "something failed" — every one of the three windows individually...
      expect(
        output,
        `${probe.file} did not fail: the guard no longer reports an attempt from that ` +
          `window.\n--- child output ---\n${output}`,
      ).toContain(probe.file);

      // ...and failed for the RIGHT REASON. One line has to carry both the guard's own
      // report and this window's own connect target; a file that failed some other way
      // contributes neither.
      const port = PROBE_WINDOWS[probe.window];
      const reported = output
        .split('\n')
        .some((line) => line.includes(GUARD_REPORT) && line.includes(`"port":${port}`));
      expect(
        reported,
        `the child failed, but nothing in its output is the socket guard reporting the ` +
          `${probe.window} window's attempt (expected a line containing ` +
          `"${GUARD_REPORT}" and "port":${port}). A failure for any other reason means this ` +
          `test is proving "the probe run broke", not "the guard caught it" — re-review ` +
          `N6.\n--- child output ---\n${output}`,
      ).toBe(true);
    }

    expect(
      output,
      `expected all ${PROBES.length} probe files to fail.\n--- child output ---\n${output}`,
    ).toContain(`Test Files  ${PROBES.length} failed (${PROBES.length})`);
  }, 60_000);
});
