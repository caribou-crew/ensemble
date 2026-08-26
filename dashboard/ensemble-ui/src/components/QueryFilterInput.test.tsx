import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import QueryFilterInput from './QueryFilterInput';
import type { Hop } from '../api/types';
import type { FilterToken } from '../trafficFilter';

const nativeValueSetter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value')!.set!;

function type(input: HTMLInputElement, text: string) {
  nativeValueSetter.call(input, text);
  input.dispatchEvent(new Event('input', { bubbles: true }));
}

function press(input: HTMLInputElement, key: string) {
  input.dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true }));
}

const HOPS: Hop[] = [
  {
    schema: 'hop.v1',
    seq: 1,
    to: 'catalog',
    method: 'GET',
    path: '/v1/products',
    status: 200,
    t: { start: '2026-01-01T00:00:00.000Z' },
  },
  {
    schema: 'hop.v1',
    seq: 2,
    to: 'catalog',
    method: 'POST',
    path: '/v1/products',
    status: 404,
    t: { start: '2026-01-01T00:00:01.000Z' },
  },
];

describe('QueryFilterInput', () => {
  let container: HTMLDivElement;
  let root: Root;
  let pills: FilterToken[];
  let draft: string;

  function render() {
    act(() => {
      root.render(
        createElement(QueryFilterInput, {
          pills,
          onPillsChange: (next: FilterToken[]) => {
            pills = next;
            render();
          },
          draft,
          onDraftChange: (next: string) => {
            draft = next;
            render();
          },
          hops: HOPS,
          placeholder: 'filter…',
        }),
      );
    });
  }

  beforeEach(() => {
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
    pills = [];
    draft = '';
    render();
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    vi.restoreAllMocks();
  });

  const input = () => container.querySelector('.query-filter__input') as HTMLInputElement;

  it('Tab-completes a field name to its colon form while typing', () => {
    act(() => type(input(), 'stat'));
    expect(container.textContent).toContain('status:');
    act(() => press(input(), 'Tab'));
    expect(input().value).toBe('status:');
  });

  it('Tab-completes a comparison-only field with no trailing colon', () => {
    act(() => type(input(), 'siz'));
    act(() => press(input(), 'Tab'));
    expect(input().value).toBe('size');
  });

  it('turns a complete token into a pill on Tab, clearing the draft', () => {
    act(() => type(input(), 'status:404'));
    act(() => press(input(), 'Tab'));
    expect(input().value).toBe('');
    expect(pills).toEqual([{ field: 'status', op: ':', value: '404' }]);
    expect(container.textContent).toContain('status:404');
  });

  it('turns a complete token into a pill on Space too', () => {
    act(() => type(input(), 'size>10kb'));
    act(() => press(input(), ' '));
    expect(input().value).toBe('');
    expect(pills).toEqual([{ field: 'size', op: '>', value: '10kb' }]);
  });

  it('lets Space insert a literal space for plain free text', () => {
    act(() => type(input(), 'checkout'));
    act(() => press(input(), ' '));
    expect(pills).toEqual([]);
  });

  it('shows value suggestions from the hops in view once a colon field is complete, and clicking one commits a pill', () => {
    act(() => type(input(), 'status:'));
    const options = Array.from(container.querySelectorAll('.query-filter__suggestions button')).map(
      (b) => b.textContent,
    );
    expect(options).toEqual(['status:200', 'status:404']);

    const first = container.querySelector('.query-filter__suggestions button') as HTMLButtonElement;
    act(() => {
      first.dispatchEvent(new MouseEvent('mousedown', { bubbles: true, cancelable: true }));
    });
    expect(pills).toEqual([{ field: 'status', op: ':', value: '200' }]);
    expect(input().value).toBe('');
  });

  it('removes the last pill on Backspace when the draft is empty', () => {
    pills = [
      { field: 'status', op: ':', value: '404' },
      { field: 'method', op: ':', value: 'GET' },
    ];
    draft = '';
    render();
    act(() => press(input(), 'Backspace'));
    expect(pills).toEqual([{ field: 'status', op: ':', value: '404' }]);
  });

  it('removes a pill via its × button', () => {
    pills = [{ field: 'status', op: ':', value: '404' }];
    draft = '';
    render();
    const removeBtn = container.querySelector('.query-filter__pill-remove') as HTMLButtonElement;
    act(() => removeBtn.click());
    expect(pills).toEqual([]);
  });
});
