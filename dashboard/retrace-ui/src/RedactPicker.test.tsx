import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { RedactPicker } from './App';
import { redactBlastRadius } from './api/client';
import type { FieldDiff } from './api/types';

const field: FieldDiff = { scope: 'resp', path: 'account.number', type: 'changed' };

function setInput(currentValue: string, next: string) {
  const el = Array.from(container.querySelectorAll('input')).find((i) => i.value === currentValue);
  expect(el, `no input currently holding ${JSON.stringify(currentValue)}`).toBeDefined();
  const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value')!.set!;
  act(() => {
    setter.call(el!, next);
    el!.dispatchEvent(new Event('input', { bubbles: true }));
  });
}

function selectMode(next: string) {
  const select = container.querySelector('select') as HTMLSelectElement;
  const setter = Object.getOwnPropertyDescriptor(window.HTMLSelectElement.prototype, 'value')!.set!;
  act(() => {
    setter.call(select, next);
    select.dispatchEvent(new Event('change', { bubbles: true }));
  });
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

describe('the redact picker', () => {
  it('pre-fills the field name from the LEAF key, not the full dotted path', () => {
    // config.RedactKeyRules matches by bare key (core/trace/redact.go's
    // redactValue walks the tree and checks each map key on its own), so
    // seeding the box with the whole path would write a rule that matches
    // nothing.
    act(() =>
      root.render(<RedactPicker field={field} busy={false} onCancel={() => {}} onConfirm={() => {}} />),
    );
    const values = Array.from(container.querySelectorAll('input')).map((i) => i.value);
    expect(values).toContain('number');
    expect(values).not.toContain('account.number');
  });

  it('states the blast radius before the confirm, and recomputes it as the field/mode change', () => {
    act(() =>
      root.render(<RedactPicker field={field} busy={false} onCancel={() => {}} onConfirm={() => {}} />),
    );
    const radius = () => container.querySelector('.picker__radius')?.textContent ?? '';
    expect(radius()).toBe(redactBlastRadius('number', 'destroy'));
    expect(radius()).toMatch(/IRREVERSIBLY/);

    selectMode('encrypt');
    expect(radius()).toBe(redactBlastRadius('number', 'encrypt'));
    expect(radius()).toMatch(/team key/);

    setInput('number', 'ssn');
    expect(radius()).toContain('ssn');

    const confirm = Array.from(container.querySelectorAll('button')).find(
      (b) => b.textContent === 'write the rule',
    );
    const radiusEl = container.querySelector('.picker__radius');
    expect(
      radiusEl!.compareDocumentPosition(confirm!) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
  });

  it('defaults the mode to destroy and offers all three modes in order', () => {
    act(() =>
      root.render(<RedactPicker field={field} busy={false} onCancel={() => {}} onConfirm={() => {}} />),
    );
    const select = container.querySelector('select') as HTMLSelectElement;
    expect(select.value).toBe('destroy');
    expect(Array.from(select.options).map((o) => o.value)).toEqual(['destroy', 'encrypt', 'display']);
  });

  it('confirms with the edited field name, mode, and why', () => {
    let got: [string, string, string] | null = null;
    act(() =>
      root.render(
        <RedactPicker
          field={field}
          busy={false}
          onCancel={() => {}}
          onConfirm={(fieldName, mode, why) => {
            got = [fieldName, mode, why];
          }}
        />,
      ),
    );
    setInput('number', 'accountNumber');
    selectMode('encrypt');
    const whyInput = Array.from(container.querySelectorAll('input')).find(
      (i) => i.placeholder === 'optional',
    ) as HTMLInputElement;
    act(() => {
      const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value')!.set!;
      setter.call(whyInput, 'checkout total needs the real id');
      whyInput.dispatchEvent(new Event('input', { bubbles: true }));
    });

    const confirm = Array.from(container.querySelectorAll('button')).find(
      (b) => b.textContent === 'write the rule',
    );
    act(() => {
      confirm!.dispatchEvent(new MouseEvent('click', { bubbles: true }));
    });
    expect(got).toEqual(['accountNumber', 'encrypt', 'checkout total needs the real id']);
  });

  it('refuses to confirm with an empty field name', () => {
    act(() =>
      root.render(<RedactPicker field={field} busy={false} onCancel={() => {}} onConfirm={() => {}} />),
    );
    setInput('number', '   ');
    const confirm = Array.from(container.querySelectorAll('button')).find(
      (b) => b.textContent === 'write the rule',
    ) as HTMLButtonElement;
    expect(confirm.disabled).toBe(true);
  });
});
