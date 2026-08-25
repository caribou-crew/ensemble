import { describe, expect, test } from 'vitest';
import { resolveLinkTemplate } from './format';

describe('resolveLinkTemplate', () => {
  test('substitutes {{column}} placeholders from the row', () => {
    const url = resolveLinkTemplate('http://localhost:3000/modules?gadgetId={{gadget_id}}', {
      gadget_id: 'abc123',
    });
    expect(url).toBe('http://localhost:3000/modules?gadgetId=abc123');
  });

  test('resolves a placeholder naming a missing column to empty string', () => {
    const url = resolveLinkTemplate('acmewallet://card?token={{missing}}', { gadget_id: 'abc123' });
    expect(url).toBe('acmewallet://card?token=');
  });

  test('resolves a nullish column value to empty string', () => {
    const url = resolveLinkTemplate('x={{a}}', { a: null });
    expect(url).toBe('x=');
  });
});
