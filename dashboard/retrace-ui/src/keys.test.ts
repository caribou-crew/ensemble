import { describe, expect, it } from 'vitest';
import { actionFor } from './keys';

const ev = (key: string, over: Partial<Parameters<typeof actionFor>[0]> = {}) =>
  ({ key, ctrlKey: false, metaKey: false, altKey: false, target: null, ...over });

describe('actionFor', () => {
  it('maps the three verbs', () => {
    expect(actionFor(ev('a'))).toBe('accept');
    expect(actionFor(ev('r'))).toBe('reject');
    expect(actionFor(ev('u'))).toBe('rule');
  });
  it('maps navigation both by letter and by arrow', () => {
    expect(actionFor(ev('j'))).toBe('next');
    expect(actionFor(ev('ArrowDown'))).toBe('next');
    expect(actionFor(ev('k'))).toBe('prev');
  });
  it('ignores keystrokes with a modifier so browser shortcuts survive', () => {
    expect(actionFor(ev('a', { metaKey: true }))).toBeNull();
  });
  it('ignores keystrokes while typing in a field', () => {
    const input = document.createElement('input');
    expect(actionFor(ev('a', { target: input }))).toBeNull();
  });
  it('returns null for an unmapped key rather than guessing', () => {
    expect(actionFor(ev('z'))).toBeNull();
  });
});
