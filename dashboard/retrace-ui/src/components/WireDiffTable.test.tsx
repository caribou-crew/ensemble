import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import type { Entry, Section } from '../api/types';
import WireDiffTable from './WireDiffTable';

const entry = (over: Partial<Entry> = {}): Entry => ({
  method: 'GET',
  normalizedPath: '/orders/{id}',
  seqA: 1,
  seqB: 1,
  posA: 0,
  posB: 0,
  moved: false,
  truncated: false,
  classes: ['changed'],
  bodyDiff: [],
  bodyTolerated: [],
  bodyViolations: [],
  bodyIgnored: [],
  orderingChanges: [],
  headerDiff: [],
  ...over,
});

const section = (name: string | null, entries: Entry[]): Section => ({
  name,
  entries,
  counts: { changed: entries.length },
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

function render(node: React.ReactNode) {
  act(() => root.render(node));
}

const click = (el: Element) => {
  act(() => {
    el.dispatchEvent(new MouseEvent('click', { bubbles: true }));
  });
};

/** Opens the one entry row this file's fixtures render. */
function expandTheOnlyRow() {
  const toggle = container.querySelector('.wire-row__toggle');
  expect(toggle).not.toBeNull();
  click(toggle as Element);
}

describe('WireDiffTable', () => {
  it('renders a tolerated field with its matcher instead of counting it as a change', () => {
    render(
      <WireDiffTable
        sections={[
          section('checkout', [
            entry({
              bodyTolerated: [
                { scope: 'resp', path: 'placedAt', type: 'changed', a: 'T1', b: 'T2', matcher: 'iso8601' },
              ],
            }),
          ]),
        ]}
        selectedField={null}
        onSelectField={() => {}}
      />,
    );
    // The row itself must not report the tolerated field as a change: a field
    // a rule already says may vary is the OPPOSITE of a finding, and counting
    // it would put a permanent amber row in front of a reviewer who already
    // ruled on it.
    const counts = container.querySelector('.wire-row__counts')?.textContent ?? '';
    expect(counts).toContain('identical');
    expect(counts).toContain('1 tolerated');

    expandTheOnlyRow();
    const tolerated = container.querySelector('.wire-field--tolerated');
    expect(tolerated).not.toBeNull();
    // ...and it names the matcher that tolerated it, which is what tells the
    // reviewer WHY it is not a finding.
    expect(tolerated?.textContent ?? '').toContain('iso8601');
    expect(container.querySelector('.wire-field--changed')).toBeNull();
  });

  it('explains a truncated body rather than rendering an empty diff', () => {
    // A truncated entry was size-capped at capture, so its body was never
    // field diffed. An empty field list under it would say "nothing differed"
    // about a comparison that did not happen.
    render(
      <WireDiffTable
        sections={[section('checkout', [entry({ truncated: true, classes: ['changed'] })])]}
        selectedField={null}
        onSelectField={() => {}}
      />,
    );
    expandTheOnlyRow();
    const explanation = container.querySelector('.wire-row__explanation');
    expect(explanation).not.toBeNull();
    expect(explanation?.textContent ?? '').toBe('body was size-capped at capture — not field diffed');
    expect(container.querySelector('.wire-fields')).toBeNull();
  });

  it('names each section from summary.sections', () => {
    // The UI end of the marker → group → section chain. A null name is the
    // traffic recorded before any marker was placed; it says so rather than
    // being dropped or silently folded into the first named part.
    render(
      <WireDiffTable
        sections={[
          section('checkout', [entry()]),
          section('receipt', [entry({ normalizedPath: '/receipt' })]),
          section(null, [entry({ normalizedPath: '/health' })]),
        ]}
        selectedField={null}
        onSelectField={() => {}}
      />,
    );
    const names = Array.from(container.querySelectorAll('.wire-section__name')).map(
      (el) => el.textContent,
    );
    expect(names).toEqual(['checkout', 'receipt', 'before any marker']);
  });
});
