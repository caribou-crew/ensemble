import { Badge, type BadgeTone } from '../primitives';
import type { CaptureTrust, Verdict } from '../diffTypes';
import './CaptureBanner.css';

// The capture-trust verdict is banner-worthy on EVERY report surface — the
// queue row and the item screen both — because it is the one field that says
// the comparison below it may be built on a recording that is missing calls.
// A diff computed from a broken capture still renders a confident green
// "pass"; this is what stops that reading.
const TONE: Record<Verdict, BadgeTone> = {
  ok: 'green',
  suspect: 'amber',
  degraded: 'amber',
  broken: 'red',
  failed: 'red',
  // The zero value, and it ranks with the worst rather than with `ok`.
  // No production path sends it any more — serve.brokenItem used to, and N-3
  // fixed that at the source — but trace.Verdict is a Go string type whose
  // zero value is still "", so Record<Verdict, …> keeps this table TOTAL over
  // the type's domain. That is what makes a future construction path which
  // forgets the field a compile error here rather than an `undefined` tone,
  // which <Badge> paints neutral grey.
  '': 'red',
};

/** The verdict as a human reads it. "" is not a verdict anybody reached; it
 * is the absence of one, and it must not render as a blank badge that looks
 * like a rendering glitch. */
function verdictLabel(status: Verdict): string {
  return status === '' ? 'not assessed' : status;
}

function TrustLineCompact({
  side,
  trust,
  bothSides,
}: {
  side: 'a' | 'b';
  trust: CaptureTrust;
  bothSides: boolean;
}) {
  const who = bothSides ? 'reference & candidate' : side === 'a' ? 'reference' : 'this run';
  return (
    <div className={`capture-banner__line capture-banner__line--${trust.status}`}>
      <Badge tone={TONE[trust.status] ?? 'red'}>
        {who}: {verdictLabel(trust.status)}
      </Badge>
      <span className="capture-banner__summary">{trust.summary}</span>
    </div>
  );
}

/**
 * Renders the sides whose capture trust is not "ok", and nothing at all when
 * both are fine — a banner that always shows is a banner nobody reads.
 *
 * When BOTH sides carry the same status and summary (the quarantine case —
 * "capture not assessed" on reference and candidate alike), they collapse to
 * ONE line instead of two identical ones: repeating the same sentence for
 * "reference" and "this run" was the bulk of the noise the banner became.
 * The one-time hint renders once at the end, not per side.
 *
 * `detail` adds the per-reason list, worth the space on the item screen and
 * not in a queue row.
 */
export default function CaptureBanner({
  capture,
  detail = false,
}: {
  capture: { a: CaptureTrust; b: CaptureTrust };
  detail?: boolean;
}) {
  const sides = ([['a', capture.a], ['b', capture.b]] as const).filter(
    ([, trust]) => trust !== undefined && trust.status !== 'ok',
  );
  if (sides.length === 0) return null;

  // Collapse two identical sides (same status + summary) to one — the
  // quarantine case, where reference and candidate carry the same sentence.
  const bothSame =
    sides.length === 2 &&
    sides[0][1].status === sides[1][1].status &&
    sides[0][1].summary === sides[1][1].summary;
  const shown = bothSame ? [sides[0]] : sides;
  const hint = shown.find(([, t]) => t.hint)?.[1].hint;

  return (
    <div className="capture-banner">
      {shown.map(([side, trust]) => (
        <div key={side}>
          <TrustLineCompact bothSides={bothSame} side={side} trust={trust} />
          {detail && trust.reasons && trust.reasons.length > 0 ? (
            <ul className="capture-banner__reasons">
              {trust.reasons.map((r) => (
                <li key={`${r.code}:${r.detail}`}>
                  <code>{r.code}</code> {r.detail}
                </li>
              ))}
            </ul>
          ) : null}
          {detail && trust.gaps && trust.gaps.length > 0 ? (
            <ul className="capture-banner__reasons">
              {trust.gaps.map((g) => (
                <li key={`${g.from}:${g.to}`}>
                  {g.seconds}s with nothing recorded, {g.from} → {g.to}
                </li>
              ))}
            </ul>
          ) : null}
        </div>
      ))}
      {hint ? <p className="capture-banner__hint">{hint}</p> : null}
    </div>
  );
}
