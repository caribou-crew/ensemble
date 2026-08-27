import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import App from './App';
import { api, ApiError } from './api/client';
import type { RetraceQueueResponse } from './api/types';

// Group 9 task 9.4/9.5: the Retrace tab is hidden entirely when the stack has
// no `retrace:` block configured, rather than shown with a broken/empty
// state — GET /api/retrace/queue answering 501 is how the tab learns that
// (spec: ensemble-retrace-view's "no retrace: block means no tab").
describe('App: Retrace tab visibility', () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    container = document.createElement('div');
    document.body.appendChild(container);
    window.history.replaceState(null, '', '/?view=entities');
    vi.spyOn(api, 'entities').mockResolvedValue([]);
    vi.spyOn(api, 'status').mockResolvedValue([]);
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    vi.restoreAllMocks();
    window.history.replaceState(null, '', '/');
  });

  it('is absent when the queue route 501s (no retrace: block configured)', async () => {
    vi.spyOn(api, 'retraceQueue').mockRejectedValue(new ApiError(501, 'retrace not configured'));

    root = createRoot(container);
    await act(async () => {
      root.render(createElement(App));
    });
    await act(async () => {
      await Promise.resolve();
    });

    const tabs = Array.from(container.querySelectorAll('button, a')).map((el) => el.textContent);
    expect(tabs).not.toContain('Retrace');
  });

  it('is present once the queue route resolves', async () => {
    const resp: RetraceQueueResponse = { items: [], empty: 'no-runs' };
    vi.spyOn(api, 'retraceQueue').mockResolvedValue(resp);

    root = createRoot(container);
    await act(async () => {
      root.render(createElement(App));
    });
    await act(async () => {
      await Promise.resolve();
    });
    await act(async () => {
      await Promise.resolve();
    });

    const tabs = Array.from(container.querySelectorAll('button, a')).map((el) => el.textContent);
    expect(tabs).toContain('Retrace');
  });
});
