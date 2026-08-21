import { useState } from 'react';
import { api } from '../api/client';
import type { CheckpointVerdict } from '../api/types';
import './ShotCompare.css';

/** The slider is a percentage and nothing else is a valid one. Both a drag
 * outside the pane and an arrow-key scrub at the end of its travel produce
 * out-of-range numbers, and an unclamped one renders a pane wider than its
 * container — the A shot spilling over the B shot, which reads as a rendering
 * bug in the app under review. */
export function clampPosition(n: number): number {
  if (!Number.isFinite(n)) return 50;
  return Math.min(100, Math.max(0, n));
}

/** Copy for a comparison side the diff never wrote. It is an EXPLANATION and
 * not a blank pane: an empty pane in a diff viewer reads as "identical", which
 * is the one thing this surface must never say by accident. The server agrees
 * — GET /api/shots/.../diff answers 404 with this same fact rather than an
 * empty 200. */
const NO_IMAGE = 'No diff image was written for this checkpoint — the two shots did not differ, so there is nothing to overlay.';

/** Copy for a checkpoint whose CANDIDATE side was never captured at all.
 *
 * summary.go builds the "missing", "added" and "unreadable" verdicts as a
 * bare CheckpointVerdict with a zero CheckpointImages, so `images.b` is ""
 * on exactly the checkpoints a reviewer opened the flow to look at. Calling
 * api.shotUrl with that empty name throws, and a throw HERE is in the render
 * phase — not a rejected promise — so useAsync never sees it and React
 * unmounts the whole tree. The reviewer would get a white page in place of
 * the checkpoint that went missing.
 *
 * So this pane says which checkpoint it is and why there is nothing in it,
 * the same treatment NO_IMAGE gives the diff tab. */
function noShotCopy(checkpoint: CheckpointVerdict): string {
  return `No shot of "${checkpoint.name}" was recorded for this run (verdict: ${checkpoint.verdict}), so there is nothing to compare against the reference. A blank pane here would read as "identical", which is the one thing this checkpoint is not.`;
}

export default function ShotCompare({
  app,
  flow,
  checkpoint,
  overlay,
  onOverlayChange,
  position,
  onPositionChange,
}: {
  app: string;
  flow: string;
  checkpoint: CheckpointVerdict;
  overlay: boolean;
  onOverlayChange: (next: boolean) => void;
  position: number;
  onPositionChange: (next: number) => void;
}) {
  const [tab, setTab] = useState<'compare' | 'diff'>('compare');
  const pos = clampPosition(position);
  const images = checkpoint.images;

  const overlayAvailable = Boolean(images.overlay);
  const showOverlay = overlay && overlayAvailable;
  // The base pane: side B normally, the generated overlay when the overlay is
  // toggled on. Never a data URI — the server serves these as image/png and
  // reading every shot into the JSON document that lists them would make the
  // item route carry the pixels twice.
  const baseName = showOverlay ? 'overlay' : 'b';
  const baseSrc = showOverlay ? images.overlay : images.b;

  const scrubFromPointer = (e: React.PointerEvent<HTMLDivElement>) => {
    const rect = e.currentTarget.getBoundingClientRect();
    if (rect.width <= 0) return;
    onPositionChange(clampPosition(((e.clientX - rect.left) / rect.width) * 100));
  };

  return (
    <div className="shot-compare">
      <div className="shot-compare__bar">
        <strong className="shot-compare__name">{checkpoint.name}</strong>
        <span className="shot-compare__verdict">{checkpoint.verdict}</span>
        <span className="shot-compare__pct">{checkpoint.diffPct.toFixed(2)}% differing</span>
        <div className="shot-compare__tabs" role="tablist">
          <button
            type="button"
            role="tab"
            aria-selected={tab === 'compare'}
            className="shot-compare__tab"
            onClick={() => setTab('compare')}
          >
            A / B
          </button>
          <button
            type="button"
            role="tab"
            aria-selected={tab === 'diff'}
            className="shot-compare__tab"
            onClick={() => setTab('diff')}
          >
            diff
          </button>
        </div>
        <label className="shot-compare__overlay-toggle">
          <input
            type="checkbox"
            checked={overlay}
            disabled={!overlayAvailable}
            onChange={(e) => onOverlayChange(e.target.checked)}
          />
          overlay
        </label>
      </div>

      {tab === 'diff' ? (
        images.diff ? (
          <div className="shot-compare__pane">
            <img
              className="shot-compare__base"
              src={api.shotUrl(app, flow, 'diff', checkpoint.name)}
              alt={`diff image for ${checkpoint.name}`}
            />
          </div>
        ) : (
          <p className="shot-compare__explanation">{NO_IMAGE}</p>
        )
      ) : overlay && !overlayAvailable ? (
        <p className="shot-compare__explanation">{NO_IMAGE}</p>
      ) : !baseSrc ? (
        // The guard images.b never had. images.a, images.diff and
        // images.overlay were all guarded; the candidate side — the one that
        // is empty precisely when a checkpoint went MISSING — was not.
        <p className="shot-compare__explanation">{noShotCopy(checkpoint)}</p>
      ) : (
        <div
          className="shot-compare__pane"
          onPointerDown={scrubFromPointer}
          onPointerMove={(e) => {
            if (e.buttons === 1) scrubFromPointer(e);
          }}
        >
          <img
            className="shot-compare__base"
            src={api.shotUrl(app, flow, baseName, checkpoint.name)}
            alt={`${baseName === 'overlay' ? 'overlay' : 'this run'} shot of ${checkpoint.name}`}
          />
          {!showOverlay && images.a ? (
            <div className="shot-compare__wipe" style={{ width: `${pos}%` }}>
              <img
                className="shot-compare__reference"
                src={api.shotUrl(app, flow, 'a', checkpoint.name)}
                alt={`reference shot of ${checkpoint.name}`}
              />
            </div>
          ) : null}
          {!showOverlay ? (
            <div className="shot-compare__handle" style={{ left: `${pos}%` }} aria-hidden="true" />
          ) : null}
        </div>
      )}

      <input
        className="shot-compare__slider"
        type="range"
        min={0}
        max={100}
        value={pos}
        aria-label="A/B wipe position"
        onChange={(e) => onPositionChange(clampPosition(Number(e.target.value)))}
      />
    </div>
  );
}
