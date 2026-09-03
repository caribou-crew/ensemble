import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import type { EmptyReason, Item } from '../retraceTypes';
import QueueList from './RetraceQueueList';

const item = (over: Partial<Item> = {}): Item => ({
  app: 'web',
  flow: 'checkout',
  verdict: 'pass',
  score: 0,
  runId: '20260821T101000Z-bbbbbbb',
  when: '2026-08-21T10:10:00Z',
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

function renderQueue(items: Item[], empty: EmptyReason, showPassing = false, selected: string | null = null) {
  act(() =>
    root.render(
      <QueueList
        items={items}
        empty={empty}
        selected={selected}
        showPassing={showPassing}
        onShowPassingChange={() => {}}
        onOpen={() => {}}
      />,
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
  it('renders a healthy changed row without a gate count, where gates used to be absent', () => {
    // R-W's consumer side: `item.gates.length` is read on every row (Row and
    // reasonCode both touch it), and a healthy row's Gates is the empty array,
    // not null — so this must not throw and must not invent a reason. The
    // gate count now lives folded into the details cell and only appears
    // alongside a reason code, so a clean changed row shows neither.
    const text = renderQueue([item({ score: 2, verdict: 'changed' })], '');
    expect(text).toContain('web');
    expect(text).toContain('checkout');
    expect(text).not.toMatch(/\d+ gate/);
  });

  it('does not inline-banner capture trust in the queue row — that moved to the detail page', () => {
    // The queue is one line per app/flow: app, flow, latest verdict, a short
    // reason. The full CaptureTrust banner (reference/this-run summaries) used
    // to render under every row and buried the table; it now lives only on the
    // flow's detail page. This pins that the row does NOT reproduce the banner
    // prose even for a broken capture — a regression back to the wall of text
    // would fail here. The fixture is asymmetric on purpose (a ok, b broken).
    const text = renderQueue(
      [
        item({
          score: 2,
          verdict: 'changed',
          capture: {
            a: { status: 'ok', summary: 'capture looks complete' },
            b: { status: 'broken', summary: 'the proxy died 12s in' },
          },
        }),
      ],
      '',
    );
    // The row is present…
    expect(text).toContain('web');
    expect(text).toContain('checkout');
    // …but the capture-banner prose is not inlined into the queue.
    expect(text).not.toContain('the proxy died 12s in');
    expect(text).not.toContain('this run');
  });

  it('paints a not-compared row as a call for attention, not as a non-event', () => {
    // D1. ScoreOf gives quarantined 1000 — deliberately the top of the queue —
    // and a verdictTone with no arm for it returned undefined, which <Badge>
    // renders NEUTRAL GREY. The row sorted to the top because it demands
    // attention and was painted the colour of a non-event; colour is
    // pre-attentive and sort order is not.
    //
    // The wire value is still `quarantined`; the UI relabels it to `not
    // compared` (verdictLabel) since that word reads as "infected" to a
    // reviewer when it actually means "the comparison could not run".
    renderQueue([item({ verdict: 'quarantined', score: 1000, gates: ['side b was quarantined'] })], '');
    const badge = Array.from(container.querySelectorAll('.ds-badge')).find(
      (b) => b.textContent === 'not compared',
    );
    expect(badge).toBeDefined();
    expect(badge!.className).not.toContain('ds-badge--neutral');
    expect(badge!.className).toContain('ds-badge--amber');
  });

  it('says WHY every kind of changed row is flagged, including the three that used to strip empty', () => {
    // F7. countsStrip omitted wireMoved, unexpectedStatuses and conformance —
    // three of the counts diff.changed() keys on — so a reorder-only,
    // status-only or conformance-only flow rendered an amber "changed" badge,
    // "0 gates" and an EMPTY strip: flagged, with nothing on the row saying
    // why, and no move available but to open it and find out.
    //
    // Driven one count at a time. A fixture that set several at once could not
    // tell which of them the strip was actually reading.
    const cases: [keyof Item['counts'], RegExp][] = [
      ['pixelChanged', /1 shots/],
      ['wireChanged', /1 wire/],
      ['wireMissing', /1 wire/],
      ['wireExtra', /1 wire/],
      ['wireMoved', /1 reordered/],
      ['hopNew', /\+1 hop/],
      ['hopGone', /-1 hop/],
      ['violations', /1 violations/],
      ['unexpectedStatuses', /1 unexpected statuses/],
      ['conformance', /1 conformance/],
    ];
    for (const [key, expected] of cases) {
      const row = item({ verdict: 'changed', score: 1, counts: { ...item().counts, [key]: 1 } });
      renderQueue([row], '');
      const strip = container.querySelector('.queue-row__counts')?.textContent ?? '';
      expect(strip, `counts.${key} left the strip empty`).not.toBe('');
      expect(strip, `counts.${key} is not named in the strip`).toMatch(expected);
      expect(container.querySelector('.queue-row__app')?.textContent).toBe('web');
      expect(container.querySelector('.queue-row__flowname')?.textContent).toBe('checkout');
    }
  });

  it('lists passing rows in the same table, not collapsed under a disclosure', () => {
    // Seeing the passing rows IS the point — they prove the flow is green.
    // They render in the one worst-first table with a visible `pass` verdict,
    // no "N passing" disclosure to expand.
    const text = renderQueue(
      [item({ flow: 'cart', score: 1100, verdict: 'failed', gates: ['status 500'] }), item()],
      '',
    );
    expect(text).not.toContain('passing'); // no disclosure control
    expect(text).toContain('cart'); // the failing row
    expect(text).toContain('pass'); // the passing row's verdict is shown
    // Both rows are present as real rows (2 flow-name cells).
    expect(container.querySelectorAll('.queue-row__flowname').length).toBe(2);
  });

  it('sorts rows when a column header is clicked', () => {
    // Default order is the server's (worst first). Clicking the app header
    // sorts alphabetically by app; the rendered flow-name cells follow.
    renderQueue(
      [
        item({ app: 'zeta', flow: 'card-views', score: 1, verdict: 'changed' }),
        item({ app: 'alpha', flow: 'card-views', score: 1000, verdict: 'failed', gates: ['x'] }),
      ],
      '',
    );
    const appHeader = Array.from(container.querySelectorAll('.queue-table__sort')).find((b) =>
      b.textContent?.startsWith('app'),
    ) as HTMLButtonElement | undefined;
    expect(appHeader).toBeDefined();
    act(() => appHeader!.click());
    const apps = Array.from(container.querySelectorAll('.queue-row__app')).map((c) => c.textContent);
    expect(apps).toEqual(['alpha', 'zeta']);
  });

  // App/framework and flow are separate COLUMNS — a repo recording several
  // build variants under one .retrace/runs tree (retrace.repo.yaml's
  // `apps:` map) needs the host/framework to scan on its own, not be read
  // out of a slash-joined "app/flow" string one row at a time.
  it('renders app and flow as separate columns, not a joined string', () => {
    renderQueue([item({ app: 'ios-native', flow: 'checkout', score: 1, verdict: 'changed' })], '');
    expect(container.querySelector('.queue-row__app')?.textContent).toBe('ios-native');
    expect(container.querySelector('.queue-row__flowname')?.textContent).toBe('checkout');
  });

  // The top-level queue is the one screen a reviewer looks at without
  // opening anything — "when did this last run" has to read straight off
  // this row, or every check-in starts with a click into the surface just
  // to find that one fact.
  it('shows when the flow last ran, right on the queue row — no click-in required', () => {
    renderQueue([item({ when: '2026-08-21T10:10:00Z' })], '');
    const cell = container.querySelector('.queue-row__when');
    expect(cell?.textContent).toContain('2026');
    // formatWhen renders a real clock time, not just a date — "last ran"
    // means the timestamp, not merely which day.
    expect(cell?.textContent).toMatch(/\d{1,2}:\d{2}/);
  });

  it('falls back to the runId\'s own timestamp when the manifest never recorded one', () => {
    // Go's zero time.Time marshals to "0001-01-01T00:00:00Z" — a non-empty
    // ISO string that Date.parse happily accepts, so formatWhen's own
    // zero-cutoff fallback is what keeps this from rendering "Dec 31, 1".
    renderQueue([item({ when: '0001-01-01T00:00:00Z', runId: '20260821T101000Z-bbbbbbb' })], '');
    const cell = container.querySelector('.queue-row__when');
    expect(cell?.textContent).toContain('2026');
    expect(cell?.textContent).not.toMatch(/\b1\b/);
  });

  it('sorts by last-ran when that column header is clicked', () => {
    renderQueue(
      [
        item({ app: 'web', flow: 'older', when: '2026-08-20T09:00:00Z', score: 1, verdict: 'changed' }),
        item({ app: 'web', flow: 'newer', when: '2026-08-22T09:00:00Z', score: 1, verdict: 'changed' }),
      ],
      '',
    );
    const clickWhenHeader = () => {
      const whenHeader = Array.from(container.querySelectorAll('.queue-table__sort')).find((b) =>
        b.textContent?.startsWith('last ran'),
      ) as HTMLButtonElement | undefined;
      expect(whenHeader).toBeDefined();
      act(() => whenHeader!.click());
    };
    clickWhenHeader();
    expect(Array.from(container.querySelectorAll('.queue-row__flowname')).map((c) => c.textContent)).toEqual([
      'older',
      'newer',
    ]);
    clickWhenHeader();
    expect(Array.from(container.querySelectorAll('.queue-row__flowname')).map((c) => c.textContent)).toEqual([
      'newer',
      'older',
    ]);
  });
});
