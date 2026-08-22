import { beforeAll, expect, it } from 'vitest';
import { attemptConnection } from './attempt';

// HOLE 2: a connection attempt from `beforeAll`, which runs before the first `beforeEach`.
// Same shape as hole 1, different window.
beforeAll(() => {
  attemptConnection('before-all');
});

it('does nothing; the attempt already happened in beforeAll', () => {
  expect(true).toBe(true);
});
