import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { RulePicker } from './App';
import { RULE_BLAST_RADIUS_ALWAYS, ruleBlastRadius } from './api/client';
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
  headerIgnored: [],
};

const field: FieldDiff = { scope: 'resp', path: 'placedAt', type: 'changed' };

/** Types into a controlled React input the way a user would: through the
 * native value setter plus an `input` event, which is what React listens for.
 * Assigning `.value` alone is invisible to React's onChange. */
function setInput(currentValue: string, next: string) {
  const el = Array.from(container.querySelectorAll('input')).find((i) => i.value === currentValue);
  expect(el, `no input currently holding ${JSON.stringify(currentValue)}`).toBeDefined();
  const setter = Object.getOwnPropertyDescriptor(
    window.HTMLInputElement.prototype,
    'value',
  )!.set!;
  act(() => {
    setter.call(el!, next);
    el!.dispatchEvent(new Event('input', { bubbles: true }));
  });
}

const clearInput = (currentValue: string) => setInput(currentValue, '');

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
    expect(text).toContain(RULE_BLAST_RADIUS_ALWAYS);
    expect(RULE_BLAST_RADIUS_ALWAYS).toMatch(/EVERY flow/);
    expect(RULE_BLAST_RADIUS_ALWAYS).toMatch(/BOTH the request and the response body/);

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

  it('says the rule got WIDER the moment the path box is cleared', () => {
    // N-2. rules.Rule.Path == "" means EVERY PATH — MatchPathGlob returns
    // true for an empty glob, documented as "an unscoped rule applies to
    // every call" — and handleRule does not require the field. So the one
    // control the blast-radius copy names as the reviewer's protection is
    // the control that silently removes it.
    //
    // The assertion is that the sentence CHANGES. Reading the text with the
    // boxes filled and never clearing one is the value costume this seam
    // invites, and it is the natural test to write: the seeded state is the
    // fixture you already have.
    act(() =>
      root.render(
        <RulePicker entry={entry} field={field} busy={false} onCancel={() => {}} onConfirm={() => {}} />,
      ),
    );
    const radius = () => container.querySelector('.picker__radius')?.textContent ?? '';

    const seeded = radius();
    // The screen renders THIS function's output, not a paraphrase of it —
    // one home for the copy, so the sentence and the rule it describes
    // cannot drift.
    expect(seeded).toBe(ruleBlastRadius('GET', '/orders/{id}'));
    expect(seeded).toContain('GET');
    expect(seeded).toContain('/orders/{id}');
    expect(seeded).not.toMatch(/EVERY PATH/);

    clearInput('/orders/{id}');
    const widened = radius();
    expect(widened).not.toBe(seeded);
    expect(widened).toMatch(/EVERY PATH/);
    // And the half that is true of every wire rule is still said.
    expect(widened).toContain(RULE_BLAST_RADIUS_ALWAYS);
  });

  it('says the same about the method box, which widens in the other dimension', () => {
    // The mirror arm. A fixture that only ever clears `path` pins `path` and
    // leaves `method` free — the two boxes are the most interchangeable pair
    // on this control.
    act(() =>
      root.render(
        <RulePicker entry={entry} field={field} busy={false} onCancel={() => {}} onConfirm={() => {}} />,
      ),
    );
    const radius = () => container.querySelector('.picker__radius')?.textContent ?? '';
    const seeded = radius();

    clearInput('GET');
    const widened = radius();
    expect(widened).not.toBe(seeded);
    expect(widened).toMatch(/EVERY METHOD/);
    expect(widened).not.toMatch(/EVERY PATH/);

    // Both cleared is the widest rule the dialect can write, and it says so
    // as its own sentence rather than as either single-box warning.
    clearInput('/orders/{id}');
    const widest = radius();
    expect(widest).not.toBe(widened);
    expect(widest).toMatch(/EVERY METHOD/);
    expect(widest).toMatch(/EVERY PATH/);
    expect(widest).toMatch(/widest rule/);
  });

  it('treats a box holding only spaces as empty, because the rule does', () => {
    // " " is not a narrowing: it is not the seeded path either, and the
    // server would write it as a glob that matches nothing or everything
    // depending on the dimension. Whitespace-only counts as empty, which is
    // the direction that warns MORE.
    act(() =>
      root.render(
        <RulePicker entry={entry} field={field} busy={false} onCancel={() => {}} onConfirm={() => {}} />,
      ),
    );
    setInput('/orders/{id}', '   ');
    expect(container.querySelector('.picker__radius')?.textContent ?? '').toMatch(/EVERY PATH/);
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
