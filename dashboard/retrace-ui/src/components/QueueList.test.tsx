import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import type { EmptyReason, Item } from '../api/types';
import QueueList from './QueueList';

const item = (over: Partial<Item> = {}): Item => ({
  app: 'web',
  flow: 'checkout',
  verdict: 'pass',
  score: 0,
  runId: '20260821T101000Z-bbbbbbb',
  counts: {
    checkpoints: 1,
    pixelChanged: 0,
    wirePaired: 3,
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
  capture: {
    a: { status: 'ok', summary: 'capture looks complete' },
    b: { status: 'ok', summary: 'capture looks complete' },
  },
  gates: [],
  ...over,
});

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
});

function renderQueue(items: Item[], empty: EmptyReason) {
  act(() =>
    root.render(
      <QueueList items={items} empty={empty} selected={null} onSelect={() => {}} onOpen={() => {}} />,
    ),
  );
  return container.textContent ?? '';
}

// R-V. "No runs have been recorded yet" and "every run was reviewed and
// nothing needs attention" are different worlds, and an empty list renders
// them identically — with the reassuring one being the reading a reviewer
// defaults to. BOTH arms are pinned: a test that covered only `all-clear`
// would leave the dangerous one, the one that reads as reassurance, unpinned.
describe('the empty review queue says WHICH kind of empty it is', () => {
  it('renders no-runs as a setup step nobody has done, never as reassurance', () => {
    const text = renderQueue([], 'no-runs');
    expect(text).toContain('No runs have been recorded yet');
    expect(text).toMatch(/retrace run --flow/);
    expect(text).toMatch(/retrace ref accept --flow/);
    // The reassurance must not appear here in any form: this is the world
    // where nothing is known, and a reviewer who reads it as "clean"
    // concludes the project is fine on the strength of never having recorded
    // anything.
    expect(text).not.toMatch(/all clear/i);
    expect(text).not.toMatch(/nothing needs attention/i);
  });

  it('renders all-clear as the reassurance it is, which the server had to earn', () => {
    const text = renderQueue([], 'all-clear');
    expect(text).toMatch(/all clear/i);
    expect(text).toContain('none of them needs attention');
    expect(text).not.toMatch(/No runs have been recorded yet/);
  });

  it('renders the zero value as neither — it promises nothing', () => {
    const text = renderQueue([], '');
    expect(text).not.toMatch(/all clear/i);
    expect(text).not.toMatch(/No runs have been recorded yet/);
    expect(text).toContain('did not say why');
  });
});

describe('QueueList rows', () => {
  it('shows the gate count on a healthy row, which is where gates used to be absent', () => {
    // R-W's consumer side: `item.gates.length` on a row with nothing wrong.
    const text = renderQueue([item({ score: 2, verdict: 'changed' })], '');
    expect(text).toContain('0 gates');
  });

  it('collapses score-zero rows under a disclosure rather than listing them', () => {
    const text = renderQueue(
      [item({ flow: 'cart', score: 1100, verdict: 'failed', gates: ['status 500'] }), item()],
      '',
    );
    expect(text).toContain('1 passing');
    expect(text).toContain('web/cart');
    expect(text).not.toContain('web/checkout');
  });
});
