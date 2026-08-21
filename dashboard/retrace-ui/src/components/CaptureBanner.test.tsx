import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import type { CaptureTrust, Verdict } from '../api/types';
import CaptureBanner from './CaptureBanner';

const trust = (status: Verdict, over: Partial<CaptureTrust> = {}): CaptureTrust => ({
  status,
  summary: `capture verdict ${status}`,
  ...over,
});

let container: HTMLDivElement;
let root: Root;

beforeEach(() => {
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
});
afterEach(() => {
  act(() => root.unmount());
  container.remove();
});

function renderBanner(a: CaptureTrust, b: CaptureTrust, detail = false) {
  act(() => root.render(<CaptureBanner capture={{ a, b }} detail={detail} />));
  return container.textContent ?? '';
}

/**
 * F4. This component's own doc names the mechanism it exists to defeat: "a
 * diff computed from a broken capture still renders a confident green
 * 'pass'; this is what stops that reading." It had no test at all, and every
 * capture fixture in the suite was `{a: ok, b: ok}` — symmetric in exactly
 * the dimension under test, so it could not detect a swap, an inversion or a
 * deletion. Inverting the one condition (`!== 'ok'` → `=== 'ok'`) left all 28
 * tests green while a broken capture bannered NOTHING and an ok one
 * bannered "ok".
 *
 * So both arms are asymmetric, and both directions of the swap are driven —
 * a fixture that only ever breaks side B pins B and leaves A free.
 */
describe('CaptureBanner', () => {
  it('banners the BROKEN side and names it, when the other side is fine', () => {
    const text = renderBanner(trust('ok'), trust('broken'));
    expect(text).toContain('this run');
    expect(text).toContain('broken');
    // Not the ok side. An inversion banners "reference: ok" here.
    expect(text).not.toContain('reference');
    expect(text).not.toMatch(/verdict ok/);
  });

  it('does the same when it is the REFERENCE that is broken, not the run', () => {
    const text = renderBanner(trust('broken'), trust('ok'));
    expect(text).toContain('reference');
    expect(text).toContain('broken');
    expect(text).not.toContain('this run');
  });

  it('says nothing at all when both sides are ok — a banner that always shows is unread', () => {
    expect(renderBanner(trust('ok'), trust('ok'))).toBe('');
    expect(container.querySelector('.capture-banner')).toBeNull();
  });

  it('banners every non-ok verdict, not only the two loud ones', () => {
    // Enumerated from the type rather than listed by hand: a verdict added to
    // trace.Verdict later is covered on the day it appears.
    const NON_OK: Verdict[] = ['', 'suspect', 'degraded', 'broken', 'failed'];
    for (const status of NON_OK) {
      const text = renderBanner(trust('ok'), trust(status));
      expect(text, `verdict ${JSON.stringify(status)} did not banner`).toContain('this run');
    }
  });

  it('renders the EMPTY verdict as "not assessed", never as a blank badge', () => {
    // R-X. serve.brokenItem used to fold a zero diff.Summary into a queue row
    // for any flow that could not be diffed, so `{"status":"","summary":""}`
    // reached this component for exactly the rows that most need a human; a
    // five-member Verdict union made that fall off the end of the tone table.
    // N-3 fixed the server, so nothing sends "" today — this pins the
    // defence, which exists because trace.Verdict's zero value is still ""
    // and the next construction path onto Item.Capture has no UI protecting
    // it. Deleting the arm is a compile error; changing its tone fails here.
    const text = renderBanner(trust('ok'), { status: '', summary: '' });
    expect(text).toContain('not assessed');
    const badge = container.querySelector('.ds-badge');
    expect(badge?.className).toContain('ds-badge--red');
  });

  it('lists the reasons and gaps only where they are worth the space', () => {
    const broken = trust('degraded', {
      reasons: [{ code: 'baggage-dropped', status: 'degraded', detail: 'api lost the trace id' }],
      gaps: [{ from: 'T1', to: 'T2', seconds: 40 }],
    });
    expect(renderBanner(trust('ok'), broken, true)).toContain('api lost the trace id');
    expect(renderBanner(trust('ok'), broken, true)).toContain('40s with nothing recorded');
    // The queue row gets the verdict and the summary, and not the list.
    expect(renderBanner(trust('ok'), broken, false)).not.toContain('api lost the trace id');
  });
});
