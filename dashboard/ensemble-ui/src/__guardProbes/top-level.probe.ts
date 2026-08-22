import { expect, it } from 'vitest';
import { attemptConnection } from './attempt';

// HOLE 1: a connection attempt from a test file's own MODULE TOP LEVEL. This runs at import
// time — before any beforeAll, and before the first beforeEach. If the guard resets its
// counter in beforeEach, this attempt is counted and then wiped before any afterEach can
// read it, and the file passes with a real connection attempt inside it.
attemptConnection('top-level');

it('does nothing; the attempt above already happened at import time', () => {
  expect(true).toBe(true);
});
