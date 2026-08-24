import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import EntityView from './EntityView';
import { api } from '../api/client';
import { readParam } from '../urlState';
import type { EntityInfo } from '../api/types';

// Each cell in the Entities list table gets a small copy icon after its value — click to
// copy that cell's raw value to the clipboard (not the row, not the whole record), without
// triggering the row's own click-to-open-detail behavior. A "copied" toast confirms the
// press and clears itself after ~1s.

const ENTITIES: EntityInfo[] = [{ name: 'users', id: 'id' }];
const ROWS = [{ id: '1', email: 'a@example.com' }];

describe('EntityView: per-cell copy', () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    container = document.createElement('div');
    document.body.appendChild(container);
    vi.spyOn(api, 'entities').mockResolvedValue(ENTITIES);
    vi.spyOn(api, 'entityList').mockResolvedValue(ROWS);
    Object.defineProperty(navigator, 'clipboard', {
      value: { writeText: vi.fn().mockResolvedValue(undefined) },
      configurable: true,
    });
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    vi.restoreAllMocks();
    vi.useRealTimers();
    window.history.replaceState(null, '', '/');
  });

  it('copies the cell\'s value and does not navigate to the row', async () => {
    window.history.replaceState(null, '', '/?entity=users');
    root = createRoot(container);
    await act(async () => {
      root.render(createElement(EntityView));
    });

    const emailCell = Array.from(container.querySelectorAll('td')).find((td) => td.textContent?.includes('a@example.com'));
    const copyBtn = emailCell?.querySelector('button') as HTMLButtonElement;
    expect(copyBtn, 'expected a copy button in the email cell').toBeTruthy();

    await act(async () => {
      copyBtn.click();
      await Promise.resolve();
    });

    expect(navigator.clipboard.writeText).toHaveBeenCalledWith('a@example.com');
    expect(readParam('id'), 'clicking copy must not open the row').toBeNull();
  });

  it('shows a "copied" toast for about a second, then it clears', async () => {
    window.history.replaceState(null, '', '/?entity=users');
    root = createRoot(container);
    await act(async () => {
      root.render(createElement(EntityView));
    });

    const emailCell = Array.from(container.querySelectorAll('td')).find((td) => td.textContent?.includes('a@example.com'));
    const copyBtn = emailCell?.querySelector('button') as HTMLButtonElement;

    vi.useFakeTimers();
    await act(async () => {
      copyBtn.click();
      await Promise.resolve();
    });
    expect(emailCell?.textContent).toContain('copied');

    await act(async () => {
      await vi.advanceTimersByTimeAsync(1100);
    });
    expect(emailCell?.textContent).not.toContain('copied');
  });
});
