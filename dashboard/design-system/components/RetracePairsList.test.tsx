import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { PairItem } from '../retraceTypes';
import RetracePairsList from './RetracePairsList';

function pairItem(overrides: Partial<PairItem> = {}): PairItem {
  return {
    appA: 'web',
    flowA: 'wallet-home',
    runA: 'reference',
    appB: 'mobile',
    flowB: 'wallet-home',
    runB: '20260904T120000Z-abc1234',
    pairId: 'web__reference',
    computedAt: '2026-09-04T12:00:00Z',
    verdict: 'changed',
    counts: {
      checkpoints: 2,
      pixelChanged: 2,
      wirePaired: 3,
      wireChanged: 1,
      wireMoved: 0,
      wireMissing: 0,
      wireExtra: 0,
      violations: 0,
      hopNew: 0,
      hopGone: 0,
      unexpectedStatuses: 0,
      conformance: 0,
    },
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

describe('RetracePairsList', () => {
  it('renders one row per persisted cross-app diff and opens it on click', async () => {
    const onOpen = vi.fn();
    const pair = pairItem();
    await act(async () => {
      root.render(<RetracePairsList pairs={[pair]} onOpen={onOpen} />);
    });
    const row = container.querySelector('.pairs__row') as HTMLElement;
    expect(row).toBeTruthy();
    expect(row.textContent).toContain('web');
    expect(row.textContent).toContain('mobile');

    await act(async () => {
      row.click();
    });
    expect(onOpen).toHaveBeenCalledWith(pair);
  });

  it('shows an empty-state message when nothing has been persisted yet', async () => {
    await act(async () => {
      root.render(<RetracePairsList pairs={[]} onOpen={vi.fn()} />);
    });
    expect(container.textContent).toContain('No cross-app diffs yet');
  });

  it('displays wireMoved count when present', async () => {
    const pair = pairItem({
      counts: {
        checkpoints: 2,
        pixelChanged: 0,
        wirePaired: 3,
        wireChanged: 0,
        wireMoved: 2,
        wireMissing: 0,
        wireExtra: 0,
        violations: 0,
        hopNew: 0,
        hopGone: 0,
        unexpectedStatuses: 0,
        conformance: 0,
      },
    });
    await act(async () => {
      root.render(<RetracePairsList pairs={[pair]} onOpen={vi.fn()} />);
    });
    const row = container.querySelector('.pairs__row') as HTMLElement;
    expect(row.textContent).toContain('0 (+2 reordered)');
  });

  it('row has keyboard accessibility attributes', async () => {
    const pair = pairItem();
    await act(async () => {
      root.render(<RetracePairsList pairs={[pair]} onOpen={vi.fn()} />);
    });
    const row = container.querySelector('.pairs__row') as HTMLElement;
    expect(row.getAttribute('role')).toBe('button');
    expect(row.getAttribute('tabindex')).toBe('0');
  });
});
