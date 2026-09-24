import { describe, expect, it } from 'vitest';
import { hasBaseline, isUsable, newestOn, pairings, type Surface } from './reportData';
import type { Baseline, Source, SurfaceRun } from '@ensemble/design-system/retraceTypes';
import type { CaptureTrust } from '@ensemble/design-system/diffTypes';

function capture(status: CaptureTrust['status']): CaptureTrust {
  return { status, summary: `capture: ${status}` };
}

function ciSource(headBranch: string): Source {
  return { schema: 'v1', kind: 'ci', workflow: 'e2e', runUrl: '', sha: 'deadbee', headBranch, syncedAt: '' };
}

function run(runId: string, opts: { checkpoints?: number; status?: CaptureTrust['status']; branch?: string } = {}): SurfaceRun {
  return {
    runId,
    when: '',
    source: opts.branch ? ciSource(opts.branch) : undefined,
    capture: capture(opts.status ?? 'ok'),
    checkpoints: opts.checkpoints ?? 1,
  };
}

/** A committed-bundle baseline unless a test says otherwise — most fixtures
 * here are testing run-selection, not the baseline itself. */
function surface(s: Omit<Surface, 'baseline'> & { baseline?: Baseline }): Surface {
  return { baseline: { kind: 'bundle', runId: 'accepted' }, ...s };
}

describe('isUsable', () => {
  it('rejects a run with zero checkpoints even when the capture verdict is ok', () => {
    expect(isUsable(run('20260101T000001Z', { checkpoints: 0 }))).toBe(false);
  });

  it('rejects broken and failed capture verdicts', () => {
    expect(isUsable(run('20260101T000001Z', { status: 'broken' }))).toBe(false);
    expect(isUsable(run('20260101T000001Z', { status: 'failed' }))).toBe(false);
  });

  it('accepts ok, suspect and degraded runs with checkpoints', () => {
    expect(isUsable(run('20260101T000001Z', { status: 'ok' }))).toBe(true);
    expect(isUsable(run('20260101T000001Z', { status: 'suspect' }))).toBe(true);
    expect(isUsable(run('20260101T000001Z', { status: 'degraded' }))).toBe(true);
  });
});

describe('newestOn', () => {
  it('picks the newest run when it is usable', () => {
    const s = surface({
      app: 'uxt-web',
      flow: 'card-views',
      runs: [run('20260101T000001Z', { branch: 'main' }), run('20260101T000002Z', { branch: 'main' })],
    });
    const pick = newestOn(s, 'main');
    expect(pick.run?.runId).toBe('20260101T000002Z');
    expect(pick.newerUnusable).toBeUndefined();
  });

  it('falls back to the newest USABLE run when the newest attempt failed capture, and flags the newer failure', () => {
    const s = surface({
      app: 'uxt-rn-android',
      flow: 'card-views',
      runs: [
        run('20260101T000001Z', { branch: 'main', status: 'ok' }),
        run('20260101T000002Z', { branch: 'main', status: 'failed', checkpoints: 0 }),
      ],
    });
    const pick = newestOn(s, 'main');
    expect(pick.run?.runId).toBe('20260101T000001Z');
    expect(pick.newerUnusable?.runId).toBe('20260101T000002Z');
  });

  it('falls back to the newest run (even unusable) when nothing on the branch is usable, so the cell is never empty', () => {
    const s = surface({
      app: 'uxt-rn-android',
      flow: 'card-views',
      runs: [run('20260101T000001Z', { branch: 'main', status: 'broken', checkpoints: 0 })],
    });
    const pick = newestOn(s, 'main');
    expect(pick.run?.runId).toBe('20260101T000001Z');
    expect(pick.newerUnusable).toBeUndefined();
  });

  it('ignores runs on other branches', () => {
    const s = surface({
      app: 'uxt-web',
      flow: 'card-views',
      runs: [run('20260101T000001Z', { branch: 'other' }), run('20260101T000002Z', { branch: 'main' })],
    });
    expect(newestOn(s, 'main').run?.runId).toBe('20260101T000002Z');
  });
});

describe('hasBaseline', () => {
  it('is false when the surface has no reference at all', () => {
    const s = surface({ app: 'uxt-web', flow: 'disputes', runs: [], baseline: { kind: 'none' } });
    expect(hasBaseline(s, run('20260101T000001Z'))).toBe(false);
  });

  it('is false when the only fallback reference IS the run under review (self-diff)', () => {
    const r = run('20260101T000001Z');
    const s = surface({ app: 'uxt-web', flow: 'disputes', runs: [r], baseline: { kind: 'run', runId: r.runId } });
    expect(hasBaseline(s, r)).toBe(false);
  });

  it('is true for a committed bundle', () => {
    const s = surface({ app: 'uxt-web', flow: 'card-views', runs: [], baseline: { kind: 'bundle', runId: 'accepted' } });
    expect(hasBaseline(s, run('20260101T000001Z'))).toBe(true);
  });

  it('is true for a fallback run that differs from the run under review', () => {
    const s = surface({ app: 'uxt-web', flow: 'card-views', runs: [], baseline: { kind: 'run', runId: 'some-other-run' } });
    expect(hasBaseline(s, run('20260101T000001Z'))).toBe(true);
  });
});

describe('pairings', () => {
  it('carries newerUnusable through into the pairing for the report cell', () => {
    const surfaces: Surface[] = [
      surface({
        app: 'uxt-rn-android',
        flow: 'card-views',
        runs: [
          run('20260101T000001Z', { branch: 'main', status: 'ok' }),
          run('20260101T000002Z', { branch: 'main', status: 'failed', checkpoints: 0 }),
        ],
      }),
    ];
    const ps = pairings(surfaces, 'main', '');
    expect(ps).toHaveLength(1);
    expect(ps[0].run?.runId).toBe('20260101T000001Z');
    expect(ps[0].newerUnusable?.runId).toBe('20260101T000002Z');
  });

  it('flags noBaseline when comparing against REFERENCE and the surface has none, without setting missingBase', () => {
    const surfaces: Surface[] = [
      surface({
        app: 'uxt-web',
        flow: 'disputes',
        runs: [run('20260101T000001Z', { branch: 'main' })],
        baseline: { kind: 'none' },
      }),
    ];
    const ps = pairings(surfaces, 'main', '');
    expect(ps[0].noBaseline).toBe(true);
    expect(ps[0].missingBase).toBe(false);
  });

  it('does not set noBaseline for a branch-vs-branch comparison — that is missingBase\'s job', () => {
    const surfaces: Surface[] = [
      surface({
        app: 'uxt-web',
        flow: 'disputes',
        runs: [run('20260101T000001Z', { branch: 'main' })],
        baseline: { kind: 'none' },
      }),
    ];
    const ps = pairings(surfaces, 'main', 'other-branch');
    expect(ps[0].noBaseline).toBe(false);
    expect(ps[0].missingBase).toBe(true);
  });
});
