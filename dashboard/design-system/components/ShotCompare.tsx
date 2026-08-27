import { api } from '../api/client';
import type { CheckpointVerdict } from '../api/types';
import './ShotCompare.css';

/** Copy for a comparison side the diff never wrote. It is an EXPLANATION and
 * not a blank pane: an empty pane in a diff viewer reads as "identical", which
 * is the one thing this surface must never say by accident. The server agrees
 * — GET /api/shots/.../diff answers 404 with this same fact rather than an
 * empty 200. */
const NO_IMAGE = 'No diff image was written for this checkpoint — the two shots did not differ, so there is nothing to overlay.';

/** Copy for a checkpoint whose CANDIDATE side was never captured at all.
 *
 * summary.go builds the "missing", "added" and "unreadable" verdicts as a
 * bare CheckpointVerdict with a zero CheckpointImages, so both `images.a`
 * and `images.b` are "" on exactly the checkpoints a reviewer opened the
 * flow to look at. Calling api.shotUrl with an empty name throws, and a
 * throw HERE is in the render phase — not a rejected promise — so useAsync
 * never sees it and React unmounts the whole tree. The reviewer would get a
 * white page in place of the checkpoint that went missing.
 *
 * So this pane says which checkpoint it is and why there is nothing to
 * compare, the same treatment NO_IMAGE gives the diff cell. */
function noShotCopy(checkpoint: CheckpointVerdict): string {
  return `No shot of "${checkpoint.name}" was recorded for this run (verdict: ${checkpoint.verdict}), so there is nothing to compare against the reference. A blank pane here would read as "identical", which is the one thing this checkpoint is not.`;
}

export default function ShotCompare({
  app,
  flow,
  checkpoint,
}: {
  app: string;
  flow: string;
  checkpoint: CheckpointVerdict;
}) {
  const images = checkpoint.images;

  // Both sides are empty together, and only together — writeCheckpointImages
  // always sets A and B in the same call. A checkpoint missing one side but
  // not the other is not a shape the server produces, so there is nothing to
  // gain from handling it separately from "no shot at all".
  if (!images.a && !images.b) {
    return (
      <div className="shot-compare">
        <ShotCompareBar checkpoint={checkpoint} />
        <p className="shot-compare__explanation">{noShotCopy(checkpoint)}</p>
      </div>
    );
  }

  // The overlay earns its place in the grid only when there is something to
  // show: no overlay image was written for a checkpoint that did not differ,
  // and showing an empty/identical-looking cell there would read as "these
  // differ" when they don't.
  const showOverlay = checkpoint.diffPct > 0 && Boolean(images.overlay);

  return (
    <div className="shot-compare">
      <ShotCompareBar checkpoint={checkpoint} />
      <div className="shot-compare__grid">
        <ShotCompareCell label="original">
          {images.a ? (
            <img
              className="shot-compare__img"
              src={api.shotUrl(app, flow, 'a', checkpoint.name)}
              alt={`reference shot of ${checkpoint.name}`}
            />
          ) : (
            <p className="shot-compare__explanation">{noShotCopy(checkpoint)}</p>
          )}
        </ShotCompareCell>
        <ShotCompareCell label="current">
          {images.b ? (
            <img
              className="shot-compare__img"
              src={api.shotUrl(app, flow, 'b', checkpoint.name)}
              alt={`this run's shot of ${checkpoint.name}`}
            />
          ) : (
            <p className="shot-compare__explanation">{noShotCopy(checkpoint)}</p>
          )}
        </ShotCompareCell>
        <ShotCompareCell label="diff">
          {images.diff ? (
            <img
              className="shot-compare__img"
              src={api.shotUrl(app, flow, 'diff', checkpoint.name)}
              alt={`diff image for ${checkpoint.name}`}
            />
          ) : (
            <p className="shot-compare__explanation">{NO_IMAGE}</p>
          )}
        </ShotCompareCell>
        {showOverlay ? (
          <ShotCompareCell label="overlay">
            <img
              className="shot-compare__img"
              src={api.shotUrl(app, flow, 'overlay', checkpoint.name)}
              alt={`overlay of differences for ${checkpoint.name}`}
            />
          </ShotCompareCell>
        ) : null}
      </div>
    </div>
  );
}

function ShotCompareBar({ checkpoint }: { checkpoint: CheckpointVerdict }) {
  return (
    <div className="shot-compare__bar">
      <strong className="shot-compare__name">{checkpoint.name}</strong>
      <span className="shot-compare__verdict">{checkpoint.verdict}</span>
      <span className="shot-compare__pct">{checkpoint.diffPct.toFixed(2)}% differing</span>
    </div>
  );
}

function ShotCompareCell({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <figure className="shot-compare__cell">
      <figcaption className="shot-compare__label">{label}</figcaption>
      {children}
    </figure>
  );
}
