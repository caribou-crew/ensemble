import { describe, expect, it } from 'vitest';
import { queueQuery } from './client';

describe('queueQuery', () => {
  it('serialises an empty or absent filter to the empty string', () => {
    // The contract the whole design leans on: an unfiltered fetch is still
    // exactly `/api/queue`, byte-identical to before filters existed.
    expect(queueQuery()).toBe('');
    expect(queueQuery({})).toBe('');
    expect(queueQuery({ source: undefined, app: undefined })).toBe('');
  });

  it('omits empty fields and keeps only the ones that are set', () => {
    expect(queueQuery({ source: 'local' })).toBe('?source=local');
    expect(queueQuery({ app: 'uxt-flutter-ios' })).toBe('?app=uxt-flutter-ios');
  });

  it('composes source and build-type together', () => {
    const qs = queueQuery({ source: 'ci', app: 'uxt-flutter-ios' });
    const params = new URLSearchParams(qs.slice(1));
    expect(params.get('source')).toBe('ci');
    expect(params.get('app')).toBe('uxt-flutter-ios');
  });
});
