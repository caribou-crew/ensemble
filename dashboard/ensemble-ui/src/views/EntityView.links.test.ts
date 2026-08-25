import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, createElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import EntityView from './EntityView';
import { api } from '../api/client';
import { readParam } from '../urlState';
import type { EntityInfo } from '../api/types';

// An entity configured with `links:` renders one "open in host app" button per row per
// link — clicking resolves the template's {{column}} placeholders against that row's own
// fields and opens the result, without triggering the row's own click-to-open-detail
// behavior. http(s) targets open in a new tab; custom schemes (acmewallet://) navigate the
// current page, since window.open silently no-ops on non-http(s) schemes in most browsers.

const ENTITIES: EntityInfo[] = [
  {
    name: 'gadgets',
    id: 'gadget_id',
    links: [
      { label: 'Open in admin-console', template: 'http://localhost:3000/modules?gadgetId={{gadget_id}}' },
      { label: 'Open in Acme Wallet (mobile)', template: 'acmewallet://card?token={{gadget_id}}' },
    ],
  },
];
const ROWS = [{ gadget_id: 'abc123' }];

describe('EntityView: per-row links', () => {
  let container: HTMLDivElement;
  let root: Root;
  let openSpy: ReturnType<typeof vi.spyOn>;
  let assignSpy: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    container = document.createElement('div');
    document.body.appendChild(container);
    vi.spyOn(api, 'entities').mockResolvedValue(ENTITIES);
    vi.spyOn(api, 'entityList').mockResolvedValue(ROWS);
    openSpy = vi.spyOn(window, 'open').mockReturnValue(null);
    assignSpy = vi.fn();
    Object.defineProperty(window, 'location', {
      value: { ...window.location, assign: assignSpy },
      configurable: true,
      writable: true,
    });
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    vi.restoreAllMocks();
    window.history.replaceState(null, '', '/');
  });

  it('renders one button per configured link and does not navigate to the row', async () => {
    window.history.replaceState(null, '', '/?entity=gadgets');
    root = createRoot(container);
    await act(async () => {
      root.render(createElement(EntityView));
    });

    const row = Array.from(container.querySelectorAll('tr')).find((tr) => tr.textContent?.includes('abc123'));
    const linkButtons = row?.querySelectorAll('.entity-table__link-btn');
    expect(linkButtons?.length).toBe(2);
    expect(readParam('id'), 'clicking a link must not open the row').toBeNull();
  });

  it('resolves the template against the row and opens http(s) links in a new tab', async () => {
    window.history.replaceState(null, '', '/?entity=gadgets');
    root = createRoot(container);
    await act(async () => {
      root.render(createElement(EntityView));
    });

    const row = Array.from(container.querySelectorAll('tr')).find((tr) => tr.textContent?.includes('abc123'));
    const linkButtons = Array.from(row?.querySelectorAll('.entity-table__link-btn') ?? []) as HTMLButtonElement[];
    const adminBtn = linkButtons.find((b) => b.textContent?.includes('Open in admin-console'));

    await act(async () => {
      adminBtn?.click();
    });

    expect(openSpy).toHaveBeenCalledWith('http://localhost:3000/modules?gadgetId=abc123', '_blank', 'noopener');
  });

  it('resolves the template and navigates the current page for a custom scheme', async () => {
    window.history.replaceState(null, '', '/?entity=gadgets');
    root = createRoot(container);
    await act(async () => {
      root.render(createElement(EntityView));
    });

    const row = Array.from(container.querySelectorAll('tr')).find((tr) => tr.textContent?.includes('abc123'));
    const linkButtons = Array.from(row?.querySelectorAll('.entity-table__link-btn') ?? []) as HTMLButtonElement[];
    const walletBtn = linkButtons.find((b) => b.textContent?.includes('Open in Acme Wallet (mobile)'));

    await act(async () => {
      walletBtn?.click();
    });

    expect(assignSpy).toHaveBeenCalledWith('acmewallet://card?token=abc123');
  });
});
