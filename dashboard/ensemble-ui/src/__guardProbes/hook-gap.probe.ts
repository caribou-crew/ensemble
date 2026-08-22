import { afterAll, describe, expect, it } from 'vitest';
import { attemptConnection } from './attempt';

// HOLE 3: a connection attempt in the gap between one test's afterEach and the next test's
// beforeEach — the window a leaked interval fires in. It is modelled with an `afterAll`
// rather than a real leaked interval because `afterAll` lands in exactly that window
// DETERMINISTICALLY, where a 1ms interval would be a race; the guard cannot tell the two
// apart, since both are just "an attempt while no test is executing".
describe('first', () => {
  afterAll(() => {
    attemptConnection('hook-gap');
  });

  it('runs and finishes cleanly', () => {
    expect(true).toBe(true);
  });
});

describe('second', () => {
  it('is the test whose afterEach must report the attempt made before it started', () => {
    expect(true).toBe(true);
  });
});
