import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api, ApiError } from '../api/client';
import type { SyncCandidate } from '../api/types';
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
    ...overrides,
  };
}

let container: HTMLDivElement;
let root: Root;

beforeEach(() => {
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
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

describe('SyncPanel', () => {
  it('asks for a repo before it fetches anything', async () => {
    const spy = vi.spyOn(api, 'syncCandidates');
    renderPanel();
    await flush();
    expect(spy).not.toHaveBeenCalled();
    expect(container.querySelector('input[name="repo"]')).toBeTruthy();
  });

  it('lists candidates once a repo is entered and disables an unpullable row', async () => {
    vi.spyOn(api, 'syncCandidates').mockResolvedValue({
      candidates: [candidate({ databaseId: 1 }), candidate({ databaseId: 2, status: 'in_progress', hasArtifacts: false })],
    });
    renderPanel();
    await enterRepo('org/repo');

    expect(container.textContent).toContain('Retrace Web Replay');
    const checkboxes = Array.from(container.querySelectorAll('input[type="checkbox"]')) as HTMLInputElement[];
    expect(checkboxes).toHaveLength(2);
    expect(checkboxes[1].disabled).toBe(true);
  });

  it('pulls the selected candidates and reports the result', async () => {
    vi.spyOn(api, 'syncCandidates').mockResolvedValue({ candidates: [candidate({ databaseId: 1 })] });
    const syncSpy = vi.spyOn(api, 'sync').mockResolvedValue({ synced: ['web/card-views/run1'], skipped: [] });
    const onSynced = vi.fn();
    renderPanel(() => {}, onSynced);
    await enterRepo('org/repo');

    const checkbox = container.querySelector('input[type="checkbox"]') as HTMLInputElement;
    await act(async () => checkbox.click());
    await flush();

    const pullButton = Array.from(container.querySelectorAll('button')).find((b) => b.textContent?.includes('Pull 1 selected'));
    expect(pullButton).toBeTruthy();
    await act(async () => pullButton!.click());
    await flush();

    expect(syncSpy).toHaveBeenCalledWith('org/repo', [{ repo: 'org/repo', databaseId: 1 }]);
    expect(onSynced).toHaveBeenCalled();
    expect(container.textContent).toContain('pulled 1');
  });

  it('a pull failure shows inline rather than crashing', async () => {
    vi.spyOn(api, 'syncCandidates').mockResolvedValue({ candidates: [candidate({ databaseId: 1 })] });
    vi.spyOn(api, 'sync').mockRejectedValue(new ApiError(502, 'gh: command not found'));
    renderPanel();
    await enterRepo('org/repo');

    const checkbox = container.querySelector('input[type="checkbox"]') as HTMLInputElement;
    await act(async () => checkbox.click());
    await flush();
    const pullButton = Array.from(container.querySelectorAll('button')).find((b) => b.textContent?.includes('Pull 1 selected'));
    await act(async () => pullButton!.click());
    await flush();

    expect(container.textContent).toContain('gh: command not found');
  });
});
