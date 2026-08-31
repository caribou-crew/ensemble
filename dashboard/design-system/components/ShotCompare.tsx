import type { CheckpointVerdict } from '../diffTypes';
import './ShotCompare.css';

/**
 * Builds the URL for one comparison side's PNG — `(app, flow, side, name) =>
 * string`. Passed in rather than imported, because the two consumers of this
 * component serve shots from different routes: retrace-ui's own `retrace
 * serve` at `/api/shots/...`, ensemble-ui's embedded retrace routes at
 * `/api/retrace/shots/...`. This component only ever calls it with a
 * non-empty `name` (see the `images.a`/`images.b`/etc. guards below), so a
 * resolver does not need to handle the empty-name case itself.
 */
export type ResolveShotUrl = (app: string, flow: string, side: string, name: string) => string;

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
 * flow to look at. Building a URL from an empty name is nonsense, and doing
 * that HERE would be in the render phase — not a rejected promise — so
 * useAsync never sees any resulting failure and React unmounts the whole
 * tree. The reviewer would get a white page in place of the checkpoint that
 * went missing.
 *
 * So this pane says which checkpoint it is and why there is nothing to
 * compare, the same treatment NO_IMAGE gives the diff cell. */
function noShotCopy(checkpoint: CheckpointVerdict): string {
  return `No shot of "${checkpoint.name}" was recorded for this run (verdict: ${checkpoint.verdict}), so there is nothing to compare against the reference. A blank pane here would read as "identical", which is the one thing this checkpoint is not.`;
}

/**
 * summary.go's zero-value CheckpointVerdict.At — Go's zero time.Time,
 * serialized. `at` carries no omitempty (retrace/diff/summary.go), so it is
 * always present on the wire; this is the one value that means "the
 * candidate side has no capture timestamp," not a real moment in 1 AD.
 */
const NO_TIMESTAMP = '0001-01-01T00:00:00Z';

/** True when `checkpoint.at` is a real capture moment, not the zero value. */
function hasTimestamp(checkpoint: CheckpointVerdict): boolean {
  return Boolean(checkpoint.at) && checkpoint.at !== NO_TIMESTAMP;
}

export default function ShotCompare({
  app,
  flow,
  checkpoint,
  resolveShotUrl,
  onSeek,
}: {
  app: string;
  flow: string;
  checkpoint: CheckpointVerdict;
  resolveShotUrl: ResolveShotUrl;
  /**
   * Called with `checkpoint.at` when the reviewer asks to jump the run's
   * video evidence to this checkpoint's moment. Passed in, not resolved
   * here, the same reason resolveShotUrl is: this component has no idea
   * whether video evidence exists for this run, let alone where the
   * `<video>` element lives — the caller (which already fetches evidence
   * and holds the manifest's startedAt for the offset math) owns that.
   * Omit it, or omit it whenever no video is mounted, and no seek
   * affordance renders.
   */
  onSeek?: (atIso: string) => void;
}) {
  const images = checkpoint.images;

  // Both sides are empty together, and only together — writeCheckpointImages
  // always sets A and B in the same call. A checkpoint missing one side but
  // not the other is not a shape the server produces, so there is nothing to
  // gain from handling it separately from "no shot at all".
  if (!images.a && !images.b) {
    return (
      <div className="shot-compare">
        <ShotCompareBar checkpoint={checkpoint} onSeek={onSeek} />
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
      <ShotCompareBar checkpoint={checkpoint} onSeek={onSeek} />
      <div className="shot-compare__grid">
        <ShotCompareCell label="original">
          {images.a ? (
            <ShotLink href={resolveShotUrl(app, flow, 'a', checkpoint.name)} alt={`reference shot of ${checkpoint.name}`} />
          ) : (
            <p className="shot-compare__explanation">{noShotCopy(checkpoint)}</p>
          )}
        </ShotCompareCell>
        <ShotCompareCell label="current">
          {images.b ? (
            <ShotLink href={resolveShotUrl(app, flow, 'b', checkpoint.name)} alt={`this run's shot of ${checkpoint.name}`} />
          ) : (
            <p className="shot-compare__explanation">{noShotCopy(checkpoint)}</p>
          )}
        </ShotCompareCell>
        <ShotCompareCell label="diff">
          {images.diff ? (
            <ShotLink href={resolveShotUrl(app, flow, 'diff', checkpoint.name)} alt={`diff image for ${checkpoint.name}`} />
          ) : (
            <p className="shot-compare__explanation">{NO_IMAGE}</p>
          )}
        </ShotCompareCell>
        {showOverlay ? (
          <ShotCompareCell label="overlay">
            <ShotLink
              href={resolveShotUrl(app, flow, 'overlay', checkpoint.name)}
              alt={`overlay of differences for ${checkpoint.name}`}
            />
          </ShotCompareCell>
        ) : null}
      </div>
    </div>
  );
}

function ShotCompareBar({
  checkpoint,
  onSeek,
}: {
  checkpoint: CheckpointVerdict;
  onSeek?: (atIso: string) => void;
}) {
  return (
    <div className="shot-compare__bar">
      <strong className="shot-compare__name">{checkpoint.name}</strong>
      <span className="shot-compare__verdict">{checkpoint.verdict}</span>
      <span className="shot-compare__pct">{checkpoint.diffPct.toFixed(2)}% differing</span>
      {onSeek && hasTimestamp(checkpoint) ? (
        <button
          type="button"
          className="shot-compare__seek"
          onClick={() => onSeek(checkpoint.at)}
          title={`Jump the video to when "${checkpoint.name}" was captured`}
        >
          ▶ jump to video
        </button>
      ) : null}
    </div>
  );
}

/**
 * A shot, constrained to a sane inline height (see ShotCompare.css's
 * `max-height`) and wrapped in a link to its own unconstrained URL — a
 * full-page capture otherwise renders at its native height, which can run
 * to several thousand pixels and push wire/hops/budgets far below the
 * fold. `target="_blank"` opens the ACTUAL image (not a lightbox), so the
 * browser's own zoom/pan handles arbitrarily large shots without this
 * component needing any zoom state of its own.
 */
function ShotLink({ href, alt }: { href: string; alt: string }) {
  return (
    <a href={href} target="_blank" rel="noreferrer">
      <img className="shot-compare__img" src={href} alt={alt} />
    </a>
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
