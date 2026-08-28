import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { Entry, Section } from '../diffTypes';
import WireDiffTable from './WireDiffTable';

// Only the reveal tests below await inside act() — react-dom's act() warns
// about async updates unless this is set, and nothing else in this package
// sets it globally.
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

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
  headerIgnored: [],
  ...over,
});

const section = (name: string, entries: Entry[]): Section => ({
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
    // The UI end of the marker → group → section chain. The EMPTY-STRING name
    // is the traffic recorded before any marker was placed; it says so rather
    // than being dropped or silently folded into the first named part.
    //
    // `''` and not `null`, and the fixture is the finding: diff.Section.Name
    // is a Go `string` with a bare tag and BuildSections builds the unnamed
    // section as `buildSection("", …)`, so production can never send null.
    // Seeded with null, this test named all three sections and pinned the
    // fallback copy — it looked maximally discriminating while discriminating
    // nothing about the bytes the server emits.
    render(
      <WireDiffTable
        sections={[
          section('checkout', [entry()]),
          section('receipt', [entry({ normalizedPath: '/receipt' })]),
          section('', [entry({ normalizedPath: '/health' })]),
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

  it('names the ONE section a marker-less flow produces, which is the ordinary case', () => {
    // BuildSections returns a single section named "" whenever a run declared
    // no group markers at all (order.go:202-204) — i.e. every flow that has
    // not adopted markers. That is not an edge case at the end of the list;
    // it is what most flows send, and it rendered the entire wire plane under
    // a blank header.
    render(
      <WireDiffTable
        sections={[section('', [entry()])]}
        selectedField={null}
        onSelectField={() => {}}
      />,
    );
    const name = container.querySelector('.wire-section__name');
    expect(name?.textContent).toBe('before any marker');
    expect(name?.textContent).not.toBe('');
  });

  const encryptedEntry = () =>
    entry({
      bodyDiff: [
        { scope: 'resp', path: 'accountNumber', type: 'changed', a: '$enc:v1:aaaa', b: '$enc:v1:bbbb' },
      ],
    });

  it('renders an encrypted field masked, with a reveal affordance, by default', () => {
    render(
      <WireDiffTable
        sections={[section('checkout', [encryptedEntry()])]}
        selectedField={null}
        onSelectField={() => {}}
        onReveal={() => Promise.resolve([])}
      />,
    );
    expandTheOnlyRow();
    // Both sides of the diff carry the marker, not the plaintext — nothing
    // decrypted client-side, per design.md D6 ("no client-side crypto").
    expect(container.querySelectorAll('.redacted')).toHaveLength(2);
    expect(container.querySelector('.redacted')?.textContent).toBe('$enc:v1:aaaa');
    const reveals = container.querySelectorAll('.wire-value__reveal');
    expect(reveals).toHaveLength(2);
    expect(reveals[0].textContent).toBe('reveal');
  });

  it('has no reveal affordance for a destroyed (non-reversible) field', () => {
    // '[redacted]' means core/trace overwrote the value at capture — there is
    // no key that could ever bring it back, so no reveal control is offered.
    render(
      <WireDiffTable
        sections={[
          section('checkout', [entry({ bodyDiff: [{ scope: 'resp', path: 'password', type: 'changed', a: '[redacted]', b: '[redacted]' }] })]),
        ]}
        selectedField={null}
        onSelectField={() => {}}
        onReveal={() => Promise.resolve([])}
      />,
    );
    expandTheOnlyRow();
    expect(container.querySelectorAll('.redacted')).toHaveLength(2);
    expect(container.querySelectorAll('.wire-value__reveal')).toHaveLength(0);
  });

  it('reveal click re-fetches the field and displays the value the server sends back', async () => {
    const onReveal = vi.fn().mockResolvedValue([
      section('checkout', [
        entry({
          bodyDiff: [
            { scope: 'resp', path: 'accountNumber', type: 'changed', a: 'real-account-1234', b: '$enc:v1:bbbb' },
          ],
        }),
      ]),
    ]);
    render(
      <WireDiffTable
        sections={[section('checkout', [encryptedEntry()])]}
        selectedField={null}
        onSelectField={() => {}}
        onReveal={onReveal}
      />,
    );
    expandTheOnlyRow();
    const [revealA] = container.querySelectorAll('.wire-value__reveal');
    await act(async () => {
      revealA.dispatchEvent(new MouseEvent('click', { bubbles: true }));
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(onReveal).toHaveBeenCalledTimes(1);
    // Side a is revealed (no longer masked); side b is untouched — only the
    // field that was clicked comes back plaintext.
    expect(container.querySelector('.wire-value:not(.redacted)')?.textContent).toBe('real-account-1234');
    expect(container.querySelectorAll('.redacted')).toHaveLength(1);
    // Clicking reveal must not ALSO select the field — it sits inside the
    // row's own selection button, and a bare click bubbling up would.
    expect(container.querySelector('.wire-field--selected')).toBeNull();
  });

  it('shows "key not available" rather than an error when the re-fetch still carries the marker', async () => {
    const onReveal = vi.fn().mockResolvedValue([section('checkout', [encryptedEntry()])]);
    render(
      <WireDiffTable
        sections={[section('checkout', [encryptedEntry()])]}
        selectedField={null}
        onSelectField={() => {}}
        onReveal={onReveal}
      />,
    );
    expandTheOnlyRow();
    const [revealA] = container.querySelectorAll('.wire-value__reveal');
    await act(async () => {
      revealA.dispatchEvent(new MouseEvent('click', { bubbles: true }));
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(container.querySelector('.wire-value__reveal')?.textContent).toBe('key not available');
    // Still masked — the marker itself is never shown as though it were live.
    expect(container.querySelector('.redacted')?.textContent).toBe('$enc:v1:aaaa');
  });
});
