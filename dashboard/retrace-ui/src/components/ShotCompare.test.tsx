import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import type { CheckpointVerdict } from '../api/types';
import ShotCompare from './ShotCompare';

const checkpoint = (over: Partial<CheckpointVerdict> = {}): CheckpointVerdict => ({
  name: 'results',
  verdict: 'changed',
  diffPct: 12.5,
  diffPctFine: 11.2,
  numDiff: 400,
  images: {
    a: 'shots/results.png',
    b: 'shots/results.png',
    diff: 'diff/shots/results.png',
    overlay: 'overlay/shots/results.png',
  },
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

function render(node: React.ReactNode) {
  act(() => root.render(node));
}

const click = (el: Element) => {
  act(() => {
    el.dispatchEvent(new MouseEvent('click', { bubbles: true }));
  });
};

describe('ShotCompare', () => {
  it('clamps the slider position to 0–100', () => {
    // A drag past the edge of the pane and an arrow-key scrub at the end of
    // its travel both produce out-of-range numbers. Unclamped, the wipe pane
    // is rendered wider than its container — the reference shot spilling over
    // the run's shot, which reads as a rendering bug in the app under review
    // rather than in the reviewer's tool.
    render(
      <ShotCompare
        app="web"
        flow="search"
        checkpoint={checkpoint()}
        overlay={false}
        onOverlayChange={() => {}}
        position={140}
        onPositionChange={() => {}}
      />,
    );
    expect((container.querySelector('.shot-compare__wipe') as HTMLElement).style.width).toBe('100%');

    render(
      <ShotCompare
        app="web"
        flow="search"
        checkpoint={checkpoint()}
        overlay={false}
        onOverlayChange={() => {}}
        position={-20}
        onPositionChange={() => {}}
      />,
    );
    expect((container.querySelector('.shot-compare__wipe') as HTMLElement).style.width).toBe('0%');
  });

  it('swaps the rendered image source when the overlay toggles', () => {
    const props = {
      app: 'web',
      flow: 'search',
      checkpoint: checkpoint(),
      onOverlayChange: () => {},
      position: 50,
      onPositionChange: () => {},
    };
    render(<ShotCompare {...props} overlay={false} />);
    expect((container.querySelector('.shot-compare__base') as HTMLImageElement).getAttribute('src')).toBe(
      '/api/shots/web/search/b/results',
    );

    render(<ShotCompare {...props} overlay />);
    expect((container.querySelector('.shot-compare__base') as HTMLImageElement).getAttribute('src')).toBe(
      '/api/shots/web/search/overlay/results',
    );
  });

  it('renders the no-diff-image case as an explanation, not a blank pane', () => {
    // GET /api/shots/.../diff answers 404 with this same fact rather than an
    // empty 200, and for the same reason: a blank pane in a diff viewer reads
    // as "identical", which is the one thing this surface must never say by
    // accident.
    render(
      <ShotCompare
        app="web"
        flow="login"
        checkpoint={checkpoint({ verdict: 'ok', diffPct: 0, images: { a: 'shots/login.png', b: 'shots/login.png' } })}
        overlay={false}
        onOverlayChange={() => {}}
        position={50}
        onPositionChange={() => {}}
      />,
    );
    const diffTab = Array.from(container.querySelectorAll('.shot-compare__tab')).find(
      (b) => b.textContent === 'diff',
    );
    expect(diffTab).toBeDefined();
    click(diffTab as Element);

    const explanation = container.querySelector('.shot-compare__explanation');
    expect(explanation).not.toBeNull();
    expect(explanation?.textContent ?? '').toMatch(/did not differ/);
    expect(container.querySelector('.shot-compare__pane')).toBeNull();
  });
});
