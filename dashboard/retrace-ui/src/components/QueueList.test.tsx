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

function renderQueue(items: Item[], empty: EmptyReason, showPassing = false) {
  act(() =>
    root.render(
      <QueueList
        items={items}
        empty={empty}
        selected={null}
        showPassing={showPassing}
        onShowPassingChange={() => {}}
        onSelect={() => {}}
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
  it('shows the gate count on a healthy row, which is where gates used to be absent', () => {
    // R-W's consumer side: `item.gates.length` on a row with nothing wrong.
    const text = renderQueue([item({ score: 2, verdict: 'changed' })], '');
    expect(text).toContain('0 gates');
  });

  it('banners a broken capture on the queue row — the first of the two report surfaces', () => {
    // The brief requires EVERY report surface to banner a non-ok CaptureTrust,
    // and the queue row is where a reviewer decides whether to open the flow
    // at all. The fixture is asymmetric on purpose: side a is ok and side b is
    // broken, so a swap, an inversion or a deletion each change the output.
    // Every capture fixture in this suite used to be {a: ok, b: ok}.
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
    expect(text).toContain('this run');
    expect(text).toContain('broken');
    expect(text).toContain('the proxy died 12s in');
    expect(text).not.toContain('reference');
  });

  it('paints a quarantined row as a call for attention, not as a non-event', () => {
    // D1. ScoreOf gives quarantined 1000 — deliberately the top of the queue —
    // and a verdictTone with no arm for it returned undefined, which <Badge>
    // renders NEUTRAL GREY. The row sorted to the top because it demands
    // attention and was painted the colour of a non-event; colour is
    // pre-attentive and sort order is not.
    renderQueue([item({ verdict: 'quarantined', score: 1000, gates: ['side b was quarantined'] })], '');
    const badge = Array.from(container.querySelectorAll('.ds-badge')).find(
      (b) => b.textContent === 'quarantined',
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
      const text = renderQueue([row], '');
      const strip = container.querySelector('.queue-row__counts')?.textContent ?? '';
      expect(strip, `counts.${key} left the strip empty`).not.toBe('');
      expect(strip, `counts.${key} is not named in the strip`).toMatch(expected);
      expect(text).toContain('web/checkout');
    }
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
