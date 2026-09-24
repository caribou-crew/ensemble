import { beforeEach, describe, expect, it } from 'vitest';
import { readParam, writeParams } from './urlState';

beforeEach(() => {
  window.history.replaceState(null, '', '/');
});

describe('readParam / writeParams', () => {
  it('round-trips a value written via writeParams', () => {
    writeParams({ view: 'topology' });
    expect(readParam('view')).toBe('topology');
  });

  it('returns null for a param that was never set', () => {
    expect(readParam('nope')).toBeNull();
  });

  it('pushes one history entry per navigation, however many nav params change in a tick', async () => {
    const before = window.history.length;
    writeParams({ view: 'queue' });
    writeParams({ app: 'web', flow: 'cart' });
    expect(window.history.length).toBe(before + 1);
    await Promise.resolve();
    writeParams({ run: 'r1' });
    expect(window.history.length).toBe(before + 2);
  });

  it('patches non-navigation params without pushing a new history entry', () => {
    const before = window.history.length;
    writeParams({ a: '1' });
    writeParams({ b: '2' });
    expect(window.history.length).toBe(before);
  });

  it('preserves other existing params when patching one key', () => {
    writeParams({ a: '1', b: '2' });
    writeParams({ a: '3' });
    expect(readParam('a')).toBe('3');
    expect(readParam('b')).toBe('2');
  });

  it('a null patch value deletes the key', () => {
    writeParams({ trace: 'abc123' });
    expect(readParam('trace')).toBe('abc123');

    writeParams({ trace: null });
    expect(readParam('trace')).toBeNull();
  });

  it('deleting an already-absent key is a no-op, not an error', () => {
    expect(() => writeParams({ ghost: null })).not.toThrow();
    expect(readParam('ghost')).toBeNull();
  });
});
