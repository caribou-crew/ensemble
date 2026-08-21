import { Badge, type BadgeTone } from '@ensemble/design-system';
import type { CaptureTrust, Verdict } from '../api/types';
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
  // serve.brokenItem folds a zero diff.Summary into a queue row for any flow
  // that could not be diffed at all, so `{"status":""}` is on the wire for
  // exactly the rows a reviewer most needs to look at. Record<Verdict, …> is
  // what makes leaving this arm out a compile error rather than an
  // `undefined` tone, which <Badge> paints neutral grey.
  '': 'red',
};

/** The verdict as a human reads it. "" is not a verdict anybody reached; it
 * is the absence of one, and it must not render as a blank badge that looks
 * like a rendering glitch. */
function verdictLabel(status: Verdict): string {
  return status === '' ? 'not assessed' : status;
}

function TrustLine({ side, trust }: { side: 'a' | 'b'; trust: CaptureTrust }) {
  return (
    <div className={`capture-banner__line capture-banner__line--${trust.status}`}>
      <Badge tone={TONE[trust.status] ?? 'red'}>
        {side === 'a' ? 'reference' : 'this run'}: {verdictLabel(trust.status)}
      </Badge>
      <span className="capture-banner__summary">{trust.summary}</span>
      {trust.hint ? <span className="capture-banner__hint">{trust.hint}</span> : null}
    </div>
  );
}

/**
 * Renders the sides whose capture trust is not "ok", and nothing at all when
 * both are fine — a banner that always shows is a banner nobody reads.
 *
 * `detail` adds the per-reason list, which is worth the space on the item
 * screen and not in a queue row.
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

  return (
    <div className="capture-banner">
      {sides.map(([side, trust]) => (
        <div key={side}>
          <TrustLine side={side} trust={trust} />
          {detail && trust.reasons && trust.reasons.length > 0 ? (
            <ul className="capture-banner__reasons">
              {trust.reasons.map((r) => (
                <li key={`${r.code}:${r.detail}`}>
                  <code>{r.code}</code> {r.detail}
                  {r.hint ? <em> — {r.hint}</em> : null}
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
    </div>
  );
}
