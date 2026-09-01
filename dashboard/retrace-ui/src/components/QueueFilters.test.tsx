import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import type { QueueFilter } from '../api/client';
import QueueFilters from './QueueFilters';

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

function render(apps: string[], filter: QueueFilter, onChange: (f: QueueFilter) => void) {
  act(() => {
    root.render(<QueueFilters apps={apps} filter={filter} onChange={onChange} />);
  });
}

// Find a chip by its exact visible label (chips are buttons).
function chip(label: string): HTMLButtonElement {
  const btns = Array.from(container.querySelectorAll('button.filter-chip')) as HTMLButtonElement[];
  const found = btns.find((b) => b.textContent === label);
  if (!found) throw new Error(`no chip labelled "${label}"; have: ${btns.map((b) => b.textContent).join(', ')}`);
  return found;
}

describe('QueueFilters', () => {
  it('marks the active source and app chips as pressed', () => {
    render(['web', 'ios-native'], { source: 'ci', app: 'ios-native' }, () => {});
    expect(chip('CI').getAttribute('aria-pressed')).toBe('true');
    expect(chip('ios-native').getAttribute('aria-pressed')).toBe('true');
    // The two "all" chips are not pressed when a specific value is set.
    const allChips = Array.from(container.querySelectorAll('button.filter-chip')).filter(
      (b) => b.textContent === 'all',
    );
    expect(allChips.length).toBe(2);
    for (const b of allChips) expect(b.getAttribute('aria-pressed')).toBe('false');
  });

  it('reports the picked source without disturbing the app filter', () => {
    let got: QueueFilter | null = null;
    render(['web', 'ios-native'], { app: 'web' }, (f) => (got = f));
    act(() => chip('local').click());
    expect(got).toEqual({ app: 'web', source: 'local' });
  });

  it('clears a chip when the already-active one is clicked again', () => {
    let got: QueueFilter | null = null;
    render(['web'], { source: 'local', app: 'web' }, (f) => (got = f));
    act(() => chip('local').click());
    expect(got).toEqual({ source: undefined, app: 'web' });
  });

  it('reports the picked app as an exact key, derived from the apps actually present', () => {
    let got: QueueFilter | null = null;
    render(['web', 'ios-native', 'android-rn'], {}, (f) => (got = f));
    act(() => chip('android-rn').click());
    expect(got).toEqual({ app: 'android-rn' });
  });

  it('hides the app chip group entirely when only one app is present', () => {
    render(['web'], {}, () => {});
    expect(() => chip('web')).toThrow();
  });
});
