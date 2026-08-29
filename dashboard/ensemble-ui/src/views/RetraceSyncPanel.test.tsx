import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import RetraceSyncPanel from './RetraceSyncPanel';
import { api, ApiError } from '../api/client';
import type { RetraceCandidate, RetraceSummary } from '../api/types';

const nativeValueSetter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value')!.set!;

async function flush(turns = 8) {
  for (let i = 0; i < turns; i++) {
    // eslint-disable-next-line no-await-in-loop
    await act(async () => {
      await Promise.resolve();
    });
  }
}

function candidate(overrides: Partial<RetraceCandidate> = {}): RetraceCandidate {
  return {
    repo: 'org/repo',
    databaseId: 1,
    workflowName: 'Retrace Replay (Visual + Wire Regression)',
    headBranch: 'main',
    actor: 'octocat',
    event: 'push',
    status: 'completed',
    conclusion: 'success',
    createdAt: '2026-08-27T10:00:00Z',
    url: 'https://github.com/org/repo/actions/runs/1',
    hasArtifacts: true,
    localRuns: [],
    ...overrides,
  };
}

function summaryFor(app: string, flow: string, runId: string): RetraceSummary {
  const manifest = {
    schema: 'retrace/1',
    app,
    flow,
    runId,
    mode: 'standalone' as const,
    git: { sha: 'deadbee', branch: 'main', dirty: false },
    startedAt: '2026-08-21T10:00:00Z',
    finishedAt: '2026-08-21T10:00:05Z',
    checkpoints: [],
    groups: [],
    capture: { status: 'ok' as const, summary: 'ok' },
    wire: { calls: 1, recorded: true },
    test: { command: '', exitCode: 0, durationMs: 0 },
    env: { go: '', platform: '', retrace: '' },
  };
  return {
    schema: 'retrace-diff/1',
    app,
    flow,
    verdict: 'pass',
    a: { runId: 'ref', kind: 'bundle', dir: '/tmp/ref', manifest },
    b: { runId, kind: 'run', dir: '/tmp/cand', manifest },
    quarantined: [],
    checkpoints: [],
    wire: { paired: [], missing: [], extra: [] },
    sections: [],
    hops: { serviceCounts: [], newRoutes: [], goneRoutes: [], hopRequireConfigured: false },
    unexpectedStatuses: [],
    perf: { status: 'unset', measuredMs: 0, budgetMs: 0 },
    conformance: [],
    openApiConfigured: false,
    capture: { a: { status: 'ok', summary: 'ok' }, b: { status: 'ok', summary: 'ok' } },
    counts: {
      checkpoints: 0,
      pixelChanged: 0,
      wirePaired: 0,
      wireChanged: 0,
      wireMoved: 0,
      wireMissing: 0,
      wireExtra: 0,
      violations: 0,
      hopNew: 0,
      hopGone: 0,
      unexpectedStatuses: 0,
      conformance: 0,
    },
    gates: [],
    budgets: [],
    unmeasuredGates: [],
    suppressions: [],
    triage: { label: '', rule: 'no-signal-moved', signals: { pixel: false, wire: false, hop: false, spec: false, capture: false } },
  };
}

describe('RetraceSyncPanel', () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    container = document.createElement('div');
    document.body.appendChild(container);
    // DetailPane's EvidenceSection fetches independently of the item
    // summary (video/report attach after a run finishes) — stubbed so the
    // tests that reach the detail view don't issue a real network call.
    vi.spyOn(api, 'retraceEvidence').mockResolvedValue({ videos: [], hasReport: false });
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    vi.restoreAllMocks();
  });

  async function render(onClose = () => {}, onSynced = () => {}) {
    root = createRoot(container);
    await act(async () => {
      root.render(createElement(RetraceSyncPanel, { onClose, onSynced }));
    });
    await flush();
  }

  function candidateRow(): HTMLTableRowElement {
    return container.querySelector('.retrace-sync-panel__row--clickable') as HTMLTableRowElement;
  }

  it('lists candidates and marks a row that cannot be pulled as unclickable', async () => {
    vi.spyOn(api, 'retraceSyncCandidates').mockResolvedValue({
      candidates: [candidate({ databaseId: 1 }), candidate({ databaseId: 2, status: 'in_progress', hasArtifacts: false })],
    });
    await render();

    expect(container.textContent).toContain('Retrace Replay');
    const rows = container.querySelectorAll('.retrace-sync-panel__row');
    expect(rows).toHaveLength(2);
    expect(container.querySelectorAll('.retrace-sync-panel__row--clickable')).toHaveLength(1);
    expect(container.querySelectorAll('.retrace-sync-panel__row--unpullable')).toHaveLength(1);
  });

  it('filters candidates by text', async () => {
    vi.spyOn(api, 'retraceSyncCandidates').mockResolvedValue({
      candidates: [candidate({ databaseId: 1, workflowName: 'Retrace Replay' }), candidate({ databaseId: 2, workflowName: 'Maestro iOS' })],
    });
    await render();

    const input = container.querySelector('input[type="text"]') as HTMLInputElement;
    await act(async () => {
      nativeValueSetter.call(input, 'maestro');
      input.dispatchEvent(new Event('input', { bubbles: true }));
    });
    await flush();

    expect(container.textContent).toContain('Maestro iOS');
    expect(container.textContent).not.toContain('Retrace Replay');
  });

  it('clicking an already-pulled candidate opens its detail view directly, with no sync call', async () => {
    vi.spyOn(api, 'retraceSyncCandidates').mockResolvedValue({
      candidates: [candidate({ databaseId: 1, localRuns: ['web/checkout/run-abc'] })],
    });
    const syncSpy = vi.spyOn(api, 'retraceSync');
    vi.spyOn(api, 'retraceItemAtRun').mockResolvedValue(summaryFor('web', 'checkout', 'run-abc'));
    await render();

    expect(container.textContent).toContain('already pulled');
    await act(async () => candidateRow().click());
    await flush();

    expect(syncSpy).not.toHaveBeenCalled();
    expect(api.retraceItemAtRun).toHaveBeenCalledWith('web', 'checkout', 'run-abc');
    expect(container.textContent).toContain('back to results');
    expect(container.textContent).toContain('web/checkout');
  });

  it('clicking a not-yet-pulled candidate pulls just that one and opens the result', async () => {
    vi.spyOn(api, 'retraceSyncCandidates').mockResolvedValue({ candidates: [candidate({ databaseId: 1 })] });
    const syncSpy = vi.spyOn(api, 'retraceSync').mockResolvedValue({ synced: ['web/checkout/run-xyz'], skipped: [] });
    vi.spyOn(api, 'retraceItemAtRun').mockResolvedValue(summaryFor('web', 'checkout', 'run-xyz'));
    const onSynced = vi.fn();
    await render(() => {}, onSynced);

    expect(container.textContent).toContain('pull & view');
    await act(async () => candidateRow().click());
    await flush();

    expect(syncSpy).toHaveBeenCalledWith([{ repo: 'org/repo', databaseId: 1 }]);
    expect(onSynced).toHaveBeenCalled();
    expect(container.textContent).toContain('back to results');
    expect(container.textContent).toContain('web/checkout');
  });

  it('a CI run that produced multiple flows shows a chooser before opening either', async () => {
    vi.spyOn(api, 'retraceSyncCandidates').mockResolvedValue({
      candidates: [candidate({ databaseId: 1, localRuns: ['web/checkout/run-abc', 'web/search/run-abc'] })],
    });
    vi.spyOn(api, 'retraceItemAtRun').mockResolvedValue(summaryFor('web', 'search', 'run-abc'));
    await render();

    await act(async () => candidateRow().click());
    await flush();

    expect(container.textContent).toContain('produced 2 flows');
    const options = Array.from(container.querySelectorAll('.retrace-sync-panel__chooser-option')) as HTMLButtonElement[];
    expect(options.map((o) => o.textContent)).toEqual(['web/checkout', 'web/search']);

    await act(async () => options[1].click());
    await flush();

    expect(api.retraceItemAtRun).toHaveBeenCalledWith('web', 'search', 'run-abc');
    expect(container.textContent).toContain('back to results');
  });

  it('back from the detail view returns to the candidate list', async () => {
    vi.spyOn(api, 'retraceSyncCandidates').mockResolvedValue({
      candidates: [candidate({ databaseId: 1, localRuns: ['web/checkout/run-abc'] })],
    });
    vi.spyOn(api, 'retraceItemAtRun').mockResolvedValue(summaryFor('web', 'checkout', 'run-abc'));
    await render();

    await act(async () => candidateRow().click());
    await flush();
    expect(container.textContent).toContain('back to results');

    const back = Array.from(container.querySelectorAll('button')).find((b) => b.textContent?.includes('back to results'));
    await act(async () => back!.click());
    await flush();

    expect(container.textContent).toContain('Retrace Replay');
    expect(container.querySelector('.retrace-sync-panel__row')).toBeTruthy();
  });

  it('refresh keeps the current list on screen and asks only for runs newer than the newest one known', async () => {
    const spy = vi
      .spyOn(api, 'retraceSyncCandidates')
      .mockResolvedValueOnce({
        candidates: [
          candidate({ databaseId: 1, createdAt: '2026-08-28T20:00:00Z' }),
          candidate({ databaseId: 2, createdAt: '2026-08-28T21:00:00Z' }),
        ],
      })
      .mockResolvedValueOnce({
        candidates: [candidate({ databaseId: 3, createdAt: '2026-08-28T23:00:00Z' })],
      });
    await render();

    const refreshButton = Array.from(container.querySelectorAll('button')).find((b) => b.textContent?.includes('refresh'));
    expect(refreshButton).toBeTruthy();
    await act(async () => {
      refreshButton!.click();
    });
    await flush();

    const rows = container.querySelectorAll('.retrace-sync-panel__row');
    expect(rows).toHaveLength(3);

    const secondCallFilters = spy.mock.calls[1][0] as { since?: string } | undefined;
    expect(secondCallFilters?.since).toBeTruthy();
  });

  it('a pull failure shows inline rather than crashing', async () => {
    vi.spyOn(api, 'retraceSyncCandidates').mockResolvedValue({ candidates: [candidate({ databaseId: 1 })] });
    vi.spyOn(api, 'retraceSync').mockRejectedValue(new ApiError(502, 'gh: command not found'));
    await render();

    await act(async () => candidateRow().click());
    await flush();

    expect(container.textContent).toContain('gh: command not found');
  });

  it('a pull that produces no flows reports why rather than opening nothing silently', async () => {
    vi.spyOn(api, 'retraceSyncCandidates').mockResolvedValue({ candidates: [candidate({ databaseId: 1 })] });
    vi.spyOn(api, 'retraceSync').mockResolvedValue({
      synced: [],
      skipped: [{ artifact: 'web-checkout', reason: 'no valid artifacts found to download' }],
    });
    await render();

    await act(async () => candidateRow().click());
    await flush();

    expect(container.textContent).toContain('no valid artifacts found to download');
    expect(container.querySelector('.retrace-sync-panel__detail')).toBeFalsy();
  });
});
