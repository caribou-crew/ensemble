import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { RulePicker } from './App';
import { RULE_BLAST_RADIUS } from './api/client';
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
