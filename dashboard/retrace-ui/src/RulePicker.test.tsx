import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { RulePicker } from './App';
import { RULE_BLAST_RADIUS } from './api/client';
import { DEFAULT_MATCHER, MATCHER_NAMES } from './api/matchers';
import type { Entry, FieldDiff } from './api/types';

const entry: Entry = {
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
};

const field: FieldDiff = { scope: 'resp', path: 'placedAt', type: 'changed' };

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

// R-U's second half. Dropping `scope` from the request makes the call
// succeed, and that is the smaller half: the user selected a RESPONSE-body
// field and asked to silence it, and what actually gets written silences that
// field name in every flow in the project and in both bodies. The server says
// so in its 400 — but a reviewer who receives that sentence AFTER clicking has
// already formed the belief that they scoped it.
describe('the rule picker', () => {
  it('states the blast radius before the confirm, not after', () => {
    act(() =>
      root.render(
        <RulePicker
          entry={entry}
          field={field}
          busy={false}
          onCancel={() => {}}
          onConfirm={() => {}}
        />,
      ),
    );
    const text = container.textContent ?? '';
    expect(text).toContain(RULE_BLAST_RADIUS);
    expect(RULE_BLAST_RADIUS).toMatch(/EVERY flow/);
    expect(RULE_BLAST_RADIUS).toMatch(/BOTH the request and the response body/);

    // And it is said BEFORE the confirm: the sentence is in the dialog that
    // the confirm button lives in, and it is rendered ahead of it.
    const radius = container.querySelector('.picker__radius');
    const confirm = Array.from(container.querySelectorAll('button')).find(
      (b) => b.textContent === 'write the rule',
    );
    expect(radius).not.toBeNull();
    expect(confirm).toBeDefined();
    expect(
      radius!.compareDocumentPosition(confirm!) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
  });

  it('offers the matcher as a closed set and opens on a member of it', () => {
    // F6. The matcher field shipped as a free-text box defaulting to "any",
    // which rules.ParseMatcher does not accept and config.AppendWireRule
    // validates before writing — so every rule written without editing that
    // box answered 400. The verb was broken on the path nobody edits.
    //
    // The old test read all three inputs and asserted only method and path,
    // so the matcher's value passed through the assertion untouched.
    let wrote: string | null = null;
    act(() =>
      root.render(
        <RulePicker
          entry={entry}
          field={field}
          busy={false}
          onCancel={() => {}}
          onConfirm={(matcher) => {
            wrote = matcher;
          }}
        />,
      ),
    );

    const select = container.querySelector('select.picker__matcher') as HTMLSelectElement | null;
    expect(select).not.toBeNull();
    // Every option, in order, and nothing else — a select cannot be typo'd,
    // but it can be seeded from a hand-written list that drifted.
    expect(Array.from(select!.options).map((o) => o.value)).toEqual([...MATCHER_NAMES]);
    // And it OPENS on a dialect member, which is the assertion the shipped
    // bug fails. Asserted against the list rather than against the literal
    // 'exact', so the two cannot be changed apart.
    expect(MATCHER_NAMES).toContain(select!.value);
    expect(select!.value).toBe(DEFAULT_MATCHER);

    // The unedited default is what actually gets written.
    const confirm = Array.from(container.querySelectorAll('button')).find(
      (b) => b.textContent === 'write the rule',
    );
    act(() => {
      confirm!.dispatchEvent(new MouseEvent('click', { bubbles: true }));
    });
    expect(MATCHER_NAMES).toContain(wrote as unknown as string);
  });

  it('seeds method and path from the selected entry — the only two dimensions a rule has', () => {
    act(() =>
      root.render(
        <RulePicker
          entry={entry}
          field={field}
          busy={false}
          onCancel={() => {}}
          onConfirm={() => {}}
        />,
      ),
    );
    const values = Array.from(container.querySelectorAll('input')).map((i) => i.value);
    expect(values).toContain('GET');
    expect(values).toContain('/orders/{id}');
  });
});
