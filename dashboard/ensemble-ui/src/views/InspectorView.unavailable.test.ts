import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import InspectorView from './InspectorView';
import { api, ApiError } from '../api/client';

// happy-dom has no EventSource; the SSE subscription is orthogonal to what this test
// exercises (same stand-in as InspectorView.stale-rows.test.ts).
vi.mock('../api/sse', () => ({
  subscribeChanges: () => () => {},
}));

// Final review F10: `useDatabases`'s `unavailable` flag went from stored state to a derived
// `error instanceof ApiError && error.status === 501` expression. The review's mutation M12
// (501 -> 599) left the whole suite green, so nothing pinned that a 501 renders the
// "inspection isn't configured for this stack" empty state rather than the red `offline`
// banner a genuine failure gets.

describe('InspectorView: 501 renders "unavailable", not "offline"', () => {
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

  it('a 501 from /api/databases renders the empty "unavailable" state', async () => {
    vi.spyOn(api, 'databases').mockRejectedValue(new ApiError(501, 'inspection not configured'));

    root = createRoot(container);
    await act(async () => {
      root.render(createElement(InspectorView));
    });

    expect(container.innerHTML).toContain('unavailable');
    expect(container.innerHTML).toContain("inspection isn't configured for this stack");
    expect(container.querySelector('.inspector-view--error')).toBeNull();
  });

  it('a genuine failure (not 501) renders the red "offline" banner instead', async () => {
    vi.spyOn(api, 'databases').mockRejectedValue(new ApiError(500, 'boom'));

    root = createRoot(container);
    await act(async () => {
      root.render(createElement(InspectorView));
    });

    expect(container.querySelector('.inspector-view--error')).toBeTruthy();
    expect(container.innerHTML).toContain('offline');
    expect(container.innerHTML).toContain('boom');
    expect(container.innerHTML).not.toContain("inspection isn't configured for this stack");
  });
});
