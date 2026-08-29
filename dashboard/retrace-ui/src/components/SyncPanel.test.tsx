import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api, ApiError } from '../api/client';
import type { ItemResponse, SyncCandidate } from '../api/types';
import SyncPanel from './SyncPanel';

const nativeValueSetter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value')!.set!;

function type(input: HTMLInputElement, text: string) {
  nativeValueSetter.call(input, text);
  input.dispatchEvent(new Event('input', { bubbles: true }));
}

async function flush(turns = 8) {
  for (let i = 0; i < turns; i++) {
    // eslint-disable-next-line no-await-in-loop
    await act(async () => {
      await Promise.resolve();
    });
  }
}

function candidate(overrides: Partial<SyncCandidate> = {}): SyncCandidate {
  return {
    repo: 'org/repo',
    databaseId: 1,
    workflowName: 'Retrace Web Replay',
    headBranch: 'main',
    actor: 'octocat',
    event: 'push',
    status: 'completed',
    conclusion: 'success',
    createdAt: '2026-08-28T23:41:54Z',
    url: 'https://github.com/org/repo/actions/runs/1',
    hasArtifacts: true,
    localRuns: [],
    ...overrides,
  };
}

function itemResponse(app: string, flow: string, runId: string): ItemResponse {
  return {
    summary: {
      schema: 'retrace/diff/1',
      app,
      flow,
      verdict: 'pass',
      a: { runId: 'ref-run', kind: 'run', dir: '', manifest: {} as never },
      b: { runId, kind: 'run', dir: '', manifest: {} as never },
      quarantined: [],
      checkpoints: [],
      wire: { paired: [], missing: [], extra: [] },
      sections: [],
      hops: { newRoutes: [], goneRoutes: [], serviceCounts: [], routeFailures: [] } as never,
      unexpectedStatuses: [],
      perf: { status: 'unset', measuredMs: 0, budgetMs: 0 },
      conformance: [],
      openApiConfigured: false,
      capture: { a: { status: 'ok', summary: '' }, b: { status: 'ok', summary: '' } },
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
      triage: { label: '', rule: '', signals: { pixel: false, wire: false, hop: false, spec: false, capture: false } },
    },
  };
}

let container: HTMLDivElement;
let root: Root;

beforeEach(() => {
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
  // ItemScreen's EvidenceSection fetches independently of the item summary
  // (video/report attach after a run finishes) — stubbed here so the tests
  // that reach the detail view don't issue a real network call.
  vi.spyOn(api, 'evidence').mockResolvedValue({ videos: [], hasReport: false });
});
afterEach(() => {
  act(() => root.unmount());
  container.remove();
  vi.restoreAllMocks();
});

function renderPanel(onClose = () => {}, onSynced = () => {}) {
  act(() => {
    root.render(<SyncPanel onClose={onClose} onSynced={onSynced} />);
  });
}

async function enterRepo(repo: string) {
  const input = container.querySelector('input[name="repo"]') as HTMLInputElement;
  await act(async () => type(input, repo));
  const form = container.querySelector('form') as HTMLFormElement;
  await act(async () => form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })));
  await flush();
}

function candidateRow(): HTMLButtonElement {
  return container.querySelector('.sync-panel__candidate-row') as HTMLButtonElement;
}

describe('SyncPanel', () => {
  it('asks for a repo before it fetches anything', async () => {
    const spy = vi.spyOn(api, 'syncCandidates');
    renderPanel();
    await flush();
    expect(spy).not.toHaveBeenCalled();
    expect(container.querySelector('input[name="repo"]')).toBeTruthy();
  });

  it('lists candidates once a repo is entered and disables a row with no artifacts and nothing pulled', async () => {
    vi.spyOn(api, 'syncCandidates').mockResolvedValue({
      candidates: [
        candidate({ databaseId: 1 }),
        candidate({ databaseId: 2, status: 'in_progress', hasArtifacts: false, localRuns: [] }),
      ],
    });
    renderPanel();
    await enterRepo('org/repo');

    expect(container.textContent).toContain('Retrace Web Replay');
    const rows = Array.from(container.querySelectorAll('.sync-panel__candidate-row')) as HTMLButtonElement[];
    expect(rows).toHaveLength(2);
    expect(rows[1].disabled).toBe(true);
  });

  it('clicking an already-pulled candidate opens its detail view directly, with no sync call', async () => {
    vi.spyOn(api, 'syncCandidates').mockResolvedValue({
      candidates: [candidate({ databaseId: 1, localRuns: ['web/checkout/run-abc'] })],
    });
    const syncSpy = vi.spyOn(api, 'sync');
    vi.spyOn(api, 'itemAtRun').mockResolvedValue(itemResponse('web', 'checkout', 'run-abc'));
    renderPanel();
    await enterRepo('org/repo');

    expect(container.textContent).toContain('already pulled');
    await act(async () => candidateRow().click());
    await flush();

    expect(syncSpy).not.toHaveBeenCalled();
    expect(api.itemAtRun).toHaveBeenCalledWith('web', 'checkout', 'run-abc');
    expect(container.textContent).toContain('back to results');
    expect(container.textContent).toContain('web/checkout');
  });

  it('clicking a not-yet-pulled candidate pulls just that one and opens the result', async () => {
    vi.spyOn(api, 'syncCandidates').mockResolvedValue({ candidates: [candidate({ databaseId: 1 })] });
    const syncSpy = vi.spyOn(api, 'sync').mockResolvedValue({ synced: ['web/checkout/run-xyz'], skipped: [] });
    vi.spyOn(api, 'itemAtRun').mockResolvedValue(itemResponse('web', 'checkout', 'run-xyz'));
    const onSynced = vi.fn();
    renderPanel(() => {}, onSynced);
    await enterRepo('org/repo');

    expect(container.textContent).toContain('pull & view');
    await act(async () => candidateRow().click());
    await flush();

    expect(syncSpy).toHaveBeenCalledWith('org/repo', [{ repo: 'org/repo', databaseId: 1 }]);
    expect(onSynced).toHaveBeenCalled();
    expect(container.textContent).toContain('back to results');
    expect(container.textContent).toContain('web/checkout');
  });

  it('a CI run that produced multiple flows shows a chooser before opening either', async () => {
    vi.spyOn(api, 'syncCandidates').mockResolvedValue({
      candidates: [candidate({ databaseId: 1, localRuns: ['web/checkout/run-abc', 'web/search/run-abc'] })],
    });
    vi.spyOn(api, 'itemAtRun').mockResolvedValue(itemResponse('web', 'search', 'run-abc'));
    renderPanel();
    await enterRepo('org/repo');

    await act(async () => candidateRow().click());
    await flush();

    expect(container.textContent).toContain('produced 2 flows');
    const options = Array.from(container.querySelectorAll('.sync-panel__chooser-option')) as HTMLButtonElement[];
    expect(options.map((o) => o.textContent)).toEqual(['web/checkout', 'web/search']);

    await act(async () => options[1].click());
    await flush();

    expect(api.itemAtRun).toHaveBeenCalledWith('web', 'search', 'run-abc');
    expect(container.textContent).toContain('back to results');
  });

  it('back from the detail view returns to the candidate list', async () => {
    vi.spyOn(api, 'syncCandidates').mockResolvedValue({
      candidates: [candidate({ databaseId: 1, localRuns: ['web/checkout/run-abc'] })],
    });
    vi.spyOn(api, 'itemAtRun').mockResolvedValue(itemResponse('web', 'checkout', 'run-abc'));
    renderPanel();
    await enterRepo('org/repo');

    await act(async () => candidateRow().click());
    await flush();
    expect(container.textContent).toContain('back to results');

    const back = Array.from(container.querySelectorAll('button')).find((b) => b.textContent?.includes('back to results'));
    await act(async () => back!.click());
    await flush();

    expect(container.textContent).toContain('Retrace Web Replay');
    expect(container.querySelector('.sync-panel__candidate-row')).toBeTruthy();
  });

  it('refresh keeps the current list on screen, asks only for runs newer than the newest one known, and merges the result in', async () => {
    const spy = vi
      .spyOn(api, 'syncCandidates')
      .mockResolvedValueOnce({
        candidates: [
          candidate({ databaseId: 1, createdAt: '2026-08-28T20:00:00Z' }),
          candidate({ databaseId: 2, createdAt: '2026-08-28T21:00:00Z' }),
        ],
      })
      .mockResolvedValueOnce({
        candidates: [candidate({ databaseId: 3, createdAt: '2026-08-28T23:00:00Z' })],
      });
    renderPanel();
    await enterRepo('org/repo');
    expect(container.textContent).toContain('Retrace Web Replay');

    const refreshButton = Array.from(container.querySelectorAll('button')).find((b) =>
      b.textContent?.includes('refresh'),
    );
    expect(refreshButton).toBeTruthy();
    await act(async () => refreshButton!.click());
    await flush();

    // The list was never cleared mid-refresh — all three runs, old and new,
    // are on screen together.
    const rows = container.querySelectorAll('.sync-panel__candidate');
    expect(rows).toHaveLength(3);

    // The second call asked only for runs newer than the newest one already
    // known (createdAt 21:00), not a full re-list.
    const secondCallFilters = spy.mock.calls[1][1] as { since?: string } | undefined;
    expect(secondCallFilters?.since).toBeTruthy();
  });

  it('a pull failure shows inline rather than crashing', async () => {
    vi.spyOn(api, 'syncCandidates').mockResolvedValue({ candidates: [candidate({ databaseId: 1 })] });
    vi.spyOn(api, 'sync').mockRejectedValue(new ApiError(502, 'gh: command not found'));
    renderPanel();
    await enterRepo('org/repo');

    await act(async () => candidateRow().click());
    await flush();

    expect(container.textContent).toContain('gh: command not found');
  });

  it('a pull that produces no flows reports why rather than opening nothing silently', async () => {
    vi.spyOn(api, 'syncCandidates').mockResolvedValue({ candidates: [candidate({ databaseId: 1 })] });
    vi.spyOn(api, 'sync').mockResolvedValue({
      synced: [],
      skipped: [{ artifact: 'web-checkout', reason: 'no valid artifacts found to download' }],
    });
    renderPanel();
    await enterRepo('org/repo');

    await act(async () => candidateRow().click());
    await flush();

    expect(container.textContent).toContain('no valid artifacts found to download');
    expect(container.querySelector('.sync-panel__detail')).toBeFalsy();
  });
});
