import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import RetraceSyncPanel from './RetraceSyncPanel';
import { api, ApiError } from '../api/client';
import type { RetraceCandidate } from '../api/types';

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
    ...overrides,
  };
}

describe('RetraceSyncPanel', () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    container = document.createElement('div');
    document.body.appendChild(container);
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    vi.restoreAllMocks();
  });

  it('lists candidates and disables a row that cannot be pulled', async () => {
    vi.spyOn(api, 'retraceSyncCandidates').mockResolvedValue({
      candidates: [candidate({ databaseId: 1 }), candidate({ databaseId: 2, status: 'in_progress', hasArtifacts: false })],
    });

    root = createRoot(container);
    await act(async () => {
      root.render(createElement(RetraceSyncPanel, { onClose: () => {}, onSynced: () => {} }));
    });
    await flush();

    expect(container.textContent).toContain('Retrace Replay');
    const checkboxes = Array.from(container.querySelectorAll('input[type="checkbox"]')) as HTMLInputElement[];
    expect(checkboxes).toHaveLength(2);
    expect(checkboxes[1].disabled).toBe(true);
  });

  it('filters candidates by text', async () => {
    vi.spyOn(api, 'retraceSyncCandidates').mockResolvedValue({
      candidates: [candidate({ databaseId: 1, workflowName: 'Retrace Replay' }), candidate({ databaseId: 2, workflowName: 'Maestro iOS' })],
    });

    root = createRoot(container);
    await act(async () => {
      root.render(createElement(RetraceSyncPanel, { onClose: () => {}, onSynced: () => {} }));
    });
    await flush();

    const input = container.querySelector('input[type="text"]') as HTMLInputElement;
    await act(async () => {
      nativeValueSetter.call(input, 'maestro');
      input.dispatchEvent(new Event('input', { bubbles: true }));
    });
    await flush();

    expect(container.textContent).toContain('Maestro iOS');
    expect(container.textContent).not.toContain('Retrace Replay');
  });

  it('pulls the selected candidates and reports the result', async () => {
    vi.spyOn(api, 'retraceSyncCandidates').mockResolvedValue({ candidates: [candidate({ databaseId: 1 })] });
    const syncSpy = vi.spyOn(api, 'retraceSync').mockResolvedValue({ synced: ['web/checkout/run1'], skipped: [] });
    const onSynced = vi.fn();

    root = createRoot(container);
    await act(async () => {
      root.render(createElement(RetraceSyncPanel, { onClose: () => {}, onSynced }));
    });
    await flush();

    const checkbox = container.querySelector('input[type="checkbox"]') as HTMLInputElement;
    await act(async () => {
      checkbox.click();
    });
    await flush();

    const pullButton = Array.from(container.querySelectorAll('button')).find((b) => b.textContent?.includes('Pull 1 selected'));
    expect(pullButton).toBeTruthy();
    await act(async () => {
      pullButton!.click();
    });
    await flush();

    expect(syncSpy).toHaveBeenCalledWith([{ repo: 'org/repo', databaseId: 1 }]);
    expect(onSynced).toHaveBeenCalled();
    expect(container.textContent).toContain('pulled 1');
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

    root = createRoot(container);
    await act(async () => {
      root.render(createElement(RetraceSyncPanel, { onClose: () => {}, onSynced: () => {} }));
    });
    await flush();

    const refreshButton = Array.from(container.querySelectorAll('button')).find((b) => b.textContent?.includes('refresh'));
    expect(refreshButton).toBeTruthy();
    await act(async () => {
      refreshButton!.click();
    });
    await flush();

    const checkboxes = container.querySelectorAll('input[type="checkbox"]');
    expect(checkboxes).toHaveLength(3);

    const secondCallFilters = spy.mock.calls[1][0] as { since?: string } | undefined;
    expect(secondCallFilters?.since).toBeTruthy();
  });

  it('a pull failure shows inline rather than crashing', async () => {
    vi.spyOn(api, 'retraceSyncCandidates').mockResolvedValue({ candidates: [candidate({ databaseId: 1 })] });
    vi.spyOn(api, 'retraceSync').mockRejectedValue(new ApiError(502, 'gh: command not found'));

    root = createRoot(container);
    await act(async () => {
      root.render(createElement(RetraceSyncPanel, { onClose: () => {}, onSynced: () => {} }));
    });
    await flush();

    const checkbox = container.querySelector('input[type="checkbox"]') as HTMLInputElement;
    await act(async () => {
      checkbox.click();
    });
    await flush();
    const pullButton = Array.from(container.querySelectorAll('button')).find((b) => b.textContent?.includes('Pull 1 selected'));
    await act(async () => {
      pullButton!.click();
    });
    await flush();

    expect(container.textContent).toContain('gh: command not found');
  });
});
