import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import type { RetraceClient } from '../retraceClient';
import type { RunRow } from '../retraceTypes';
import RunsList from './RetraceRunsList';

const run = (over: Partial<RunRow> = {}): RunRow => ({
  runId: '20260821T101000Z-bbbbbbb',
  verdict: 'pass',
  when: '',
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
  gates: [],
  ...over,
});

// Only `runs` is exercised here — every other RetraceClient method throws if
// this component ever reaches for it, which would itself be the bug.
function fakeClient(runs: RunRow[]): RetraceClient {
  const unused = () => {
    throw new Error('not used by RetraceRunsList');
  };
  return {
    queue: unused,
    item: unused,
    itemAtRun: unused,
    runs: async () => ({ runs }),
    shotUrl: unused,
    shotUrlAtRun: unused,
    videoUrl: unused,
    reportUrl: unused,
    evidence: unused,
    syncConfig: unused,
    syncCandidates: unused,
    syncBranches: unused,
    sync: unused,
    pairs: unused,
    pair: unused,
    pairShotUrl: unused,
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
});

async function renderRuns(runs: RunRow[]) {
  await act(async () => {
    root.render(
      <RunsList client={fakeClient(runs)} app="web" flow="checkout" selectedRun={null} onOpenRun={() => {}} />,
    );
    await Promise.resolve();
  });
}

function whenCells(): (string | null)[] {
  return Array.from(container.querySelectorAll('.queue-row__when')).map((c) => c.textContent);
}

function sortHeader(label: string): HTMLButtonElement {
  return Array.from(container.querySelectorAll('.queue-table__sort')).find((b) =>
    b.textContent?.startsWith(label),
  ) as HTMLButtonElement;
}

// The runs-loading fetch means state updates from a click land after a
// microtask, not synchronously inside the event handler — re-dispatch
// through an async act() so React actually flushes before we assert.
async function clickHeader(label: string) {
  await act(async () => {
    sortHeader(label).dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }));
    await Promise.resolve();
  });
}

describe('RetraceRunsList sorting', () => {
  it('defaults to newest-first, top to bottom', async () => {
    await renderRuns([
      run({ runId: '20260101T000000Z-aaa', when: '2026-01-01T00:00:00Z' }),
      run({ runId: '20260301T000000Z-ccc', when: '2026-03-01T00:00:00Z' }),
      run({ runId: '20260201T000000Z-bbb', when: '2026-02-01T00:00:00Z' }),
    ]);
    const rows = Array.from(container.querySelectorAll('tbody tr'));
    expect(rows.map((r) => r.getAttribute('title'))).toEqual([
      '20260301T000000Z-ccc',
      '20260201T000000Z-bbb',
      '20260101T000000Z-aaa',
    ]);
  });

  it('sorts by a clicked column, and flips on a second click', async () => {
    await renderRuns([
      run({ runId: '20260101T000000Z-aaa', when: '2026-01-01T00:00:00Z', verdict: 'pass' }),
      run({ runId: '20260201T000000Z-bbb', when: '2026-02-01T00:00:00Z', verdict: 'failed' }),
    ]);
    expect(sortHeader('verdict')).toBeDefined();

    // First click sorts ascending by verdict rank: pass (0) before failed (3).
    await clickHeader('verdict');
    let rows = Array.from(container.querySelectorAll('tbody tr'));
    expect(rows.map((r) => r.getAttribute('title'))).toEqual(['20260101T000000Z-aaa', '20260201T000000Z-bbb']);

    // Second click flips to descending: worst (failed) first.
    await clickHeader('verdict');
    rows = Array.from(container.querySelectorAll('tbody tr'));
    expect(rows.map((r) => r.getAttribute('title'))).toEqual(['20260201T000000Z-bbb', '20260101T000000Z-aaa']);
  });

  it('returns to newest-first on a third click of the same header', async () => {
    await renderRuns([
      run({ runId: '20260101T000000Z-aaa', when: '2026-01-01T00:00:00Z' }),
      run({ runId: '20260201T000000Z-bbb', when: '2026-02-01T00:00:00Z' }),
    ]);

    await clickHeader('when'); // ascending: oldest first
    let rows = Array.from(container.querySelectorAll('tbody tr'));
    expect(rows.map((r) => r.getAttribute('title'))).toEqual(['20260101T000000Z-aaa', '20260201T000000Z-bbb']);

    await clickHeader('when'); // descending: newest first
    rows = Array.from(container.querySelectorAll('tbody tr'));
    expect(rows.map((r) => r.getAttribute('title'))).toEqual(['20260201T000000Z-bbb', '20260101T000000Z-aaa']);

    await clickHeader('when'); // third click: back to default (still newest first)
    rows = Array.from(container.querySelectorAll('tbody tr'));
    expect(rows.map((r) => r.getAttribute('title'))).toEqual(['20260201T000000Z-bbb', '20260101T000000Z-aaa']);
  });

  it('renders when cells newest to oldest, top to bottom, by default', async () => {
    await renderRuns([
      run({ runId: '20260101T000000Z-aaa', when: '2026-01-01T00:00:00Z' }),
      run({ runId: '20260301T000000Z-ccc', when: '2026-03-01T00:00:00Z' }),
    ]);
    const cells = whenCells();
    expect(cells[0]).toContain('2026');
    expect(container.querySelectorAll('tbody tr').length).toBe(2);
  });
});
