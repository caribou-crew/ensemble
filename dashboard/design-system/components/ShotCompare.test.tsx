import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { CheckpointVerdict } from '../diffTypes';
import ShotCompare, { type ResolveShotUrl } from './ShotCompare';

// A fake standing in for what a real consumer (retrace-ui, ensemble-ui) would
// wire up — see ShotCompare's own doc on why the URL shape is the caller's
// choice. This mirrors retrace-ui's `/api/shots/...` shape only because that
// is a convenient, recognizable fixture, not because ShotCompare cares.
const resolveShotUrl: ResolveShotUrl = (app, flow, side, name) =>
  `/api/shots/${app}/${flow}/${side}/${name}`;

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
  at: '0001-01-01T00:00:00Z',
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

function cellLabels(): string[] {
  return Array.from(container.querySelectorAll('.shot-compare__label')).map((el) => el.textContent ?? '');
}

describe('ShotCompare', () => {
  it('lays out original, current, diff and overlay as clearly labeled panes', () => {
    render(<ShotCompare app="web" flow="search" checkpoint={checkpoint()} resolveShotUrl={resolveShotUrl} />);

    expect(cellLabels()).toEqual(['original', 'current', 'diff', 'overlay']);
    expect((container.querySelector('.shot-compare__cell:nth-child(1) img') as HTMLImageElement).getAttribute(
      'src',
    )).toBe('/api/shots/web/search/a/results');
    expect((container.querySelector('.shot-compare__cell:nth-child(2) img') as HTMLImageElement).getAttribute(
      'src',
    )).toBe('/api/shots/web/search/b/results');
    expect((container.querySelector('.shot-compare__cell:nth-child(3) img') as HTMLImageElement).getAttribute(
      'src',
    )).toBe('/api/shots/web/search/diff/results');
    expect((container.querySelector('.shot-compare__cell:nth-child(4) img') as HTMLImageElement).getAttribute(
      'src',
    )).toBe('/api/shots/web/search/overlay/results');
  });

  it('links each shot to its own full-size URL, so a tall capture (constrained inline) can be opened at full size', () => {
    render(<ShotCompare app="web" flow="search" checkpoint={checkpoint()} resolveShotUrl={resolveShotUrl} />);

    const links = Array.from(container.querySelectorAll('.shot-compare__cell a')) as HTMLAnchorElement[];
    expect(links).toHaveLength(4);
    expect(links[0].getAttribute('href')).toBe('/api/shots/web/search/a/results');
    expect(links[0].getAttribute('target')).toBe('_blank');
    expect(links[0].querySelector('img')).not.toBeNull();
  });

  it('omits the overlay pane when the checkpoint did not differ, even if an overlay image exists', () => {
    render(
      <ShotCompare
        app="web"
        flow="search"
        checkpoint={checkpoint({ diffPct: 0, verdict: 'ok' })}
        resolveShotUrl={resolveShotUrl}
      />,
    );

    expect(cellLabels()).toEqual(['original', 'current', 'diff']);
  });

  it('omits the overlay pane when no overlay image was written, even if diffPct is nonzero', () => {
    render(
      <ShotCompare
        app="web"
        flow="search"
        checkpoint={checkpoint({ images: { a: 'a.png', b: 'b.png', diff: 'diff.png' } })}
        resolveShotUrl={resolveShotUrl}
      />,
    );

    expect(cellLabels()).toEqual(['original', 'current', 'diff']);
  });

  it('renders the no-diff-image case as an explanation in the diff cell, not a blank pane', () => {
    // GET /api/shots/.../diff answers 404 with this same fact rather than an
    // empty 200, and for the same reason: a blank pane in a diff viewer reads
    // as "identical", which is the one thing this surface must never say by
    // accident.
    render(
      <ShotCompare
        app="web"
        flow="login"
        checkpoint={checkpoint({ verdict: 'ok', diffPct: 0, images: { a: 'shots/login.png', b: 'shots/login.png' } })}
        resolveShotUrl={resolveShotUrl}
      />,
    );

    expect(cellLabels()).toEqual(['original', 'current', 'diff']);
    const diffCell = container.querySelectorAll('.shot-compare__cell')[2];
    expect(diffCell.querySelector('.shot-compare__explanation')?.textContent ?? '').toMatch(/did not differ/);
    expect(diffCell.querySelector('img')).toBeNull();
  });

  // The bytes summary.go actually emits for a checkpoint that went missing:
  // "missing", "added" and "unreadable" are constructed as a bare
  // CheckpointVerdict, every image tag is omitempty, so the JSON is
  // `"images":{}` and EVERY side is absent.
  //
  // The fixture is parsed from that JSON rather than spread over the
  // checkpoint() helper on purpose: the helper hardcodes all four images.
  const MISSING_CHECKPOINT_JSON =
    '{"name":"receipt","verdict":"missing","diffPct":0,"diffPctFine":0,"numDiff":0,"images":{},"at":"0001-01-01T00:00:00Z"}';

  it('explains a checkpoint whose candidate shot was never captured, instead of throwing out of render', () => {
    const missing = JSON.parse(MISSING_CHECKPOINT_JSON) as CheckpointVerdict;
    expect(missing.images.a).toBeUndefined();
    expect(missing.images.b).toBeUndefined();

    render(<ShotCompare app="web" flow="checkout" checkpoint={missing} resolveShotUrl={resolveShotUrl} />);

    const explanation = container.querySelector('.shot-compare__explanation');
    expect(explanation).not.toBeNull();
    // Named, so the reviewer knows WHICH checkpoint the empty pane is about,
    // and carrying the verdict, so "missing" is not mistaken for "identical".
    expect(explanation?.textContent ?? '').toContain('receipt');
    expect(explanation?.textContent ?? '').toContain('missing');
    expect(container.querySelector('.shot-compare__grid')).toBeNull();
  });

  it('does the same for "added" and "unreadable", the other two zero-image verdicts', () => {
    for (const verdict of ['added', 'unreadable'] as const) {
      const cp = JSON.parse(MISSING_CHECKPOINT_JSON) as CheckpointVerdict;
      cp.verdict = verdict;
      render(<ShotCompare app="web" flow="checkout" checkpoint={cp} resolveShotUrl={resolveShotUrl} />);
      expect(container.querySelector('.shot-compare__explanation')).not.toBeNull();
      expect(container.querySelector('.shot-compare__grid')).toBeNull();
    }
  });

  it('offers to jump the video to this checkpoint\'s moment when a real timestamp and an onSeek both exist', () => {
    const onSeek = vi.fn();
    render(
      <ShotCompare
        app="web"
        flow="search"
        checkpoint={checkpoint({ at: '2026-08-31T12:00:03Z' })}
        resolveShotUrl={resolveShotUrl}
        onSeek={onSeek}
      />,
    );

    const button = container.querySelector('.shot-compare__seek') as HTMLButtonElement | null;
    expect(button).not.toBeNull();
    act(() => button!.click());
    expect(onSeek).toHaveBeenCalledWith('2026-08-31T12:00:03Z');
  });

  it('omits the seek affordance without an onSeek, even with a real timestamp', () => {
    render(
      <ShotCompare
        app="web"
        flow="search"
        checkpoint={checkpoint({ at: '2026-08-31T12:00:03Z' })}
        resolveShotUrl={resolveShotUrl}
      />,
    );

    expect(container.querySelector('.shot-compare__seek')).toBeNull();
  });

  it('omits the seek affordance for Go\'s zero-time value, even with an onSeek — this checkpoint has no capture moment to jump to', () => {
    render(
      <ShotCompare
        app="web"
        flow="search"
        checkpoint={checkpoint({ at: '0001-01-01T00:00:00Z' })}
        resolveShotUrl={resolveShotUrl}
        onSeek={vi.fn()}
      />,
    );

    expect(container.querySelector('.shot-compare__seek')).toBeNull();
  });
});
