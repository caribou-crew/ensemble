import type { Item } from './api/types';

/**
 * Presentation only.
 *
 * There is deliberately NO TypeScript copy of serve.ScoreOf here. The server
 * computes the score, puts it on the wire as `Item.score`, and this app sorts
 * and renders that number. A mirrored formula is two implementations of one
 * rule, and the day they disagree the queue silently orders itself
 * differently from the CI report.
 *
 * TOTAL, and enforced by the compiler (D1). A tone function that can return
 * `undefined` is the zero-value hazard one level down: `<Badge tone={undefined}>`
 * does not fail, it renders NEUTRAL GREY — the colour of a non-event — so a
 * verdict this switch has no arm for is painted as the reassuring answer. The
 * `never` check below turns that into a type error at the line that adds the
 * fifth verdict, instead of a grey badge at a reviewer's desk.
 *
 * `quarantined` is AMBER, not red, and not grey. It is "could not evaluate",
 * not "evaluated and bad" — the design system's palette has no third tone that
 * says so (`neutral`, `accent` and `blue` all read as informational, which is
 * exactly the non-event reading this exists to prevent), so amber over red:
 * it is unmistakably a call for attention without asserting a failure nobody
 * actually observed. ScoreOf already sorts it to the top of the queue at 1000.
 */
export function verdictTone(v: Item['verdict']): 'green' | 'amber' | 'red' {
  switch (v) {
    case 'pass':
      return 'green';
    case 'changed':
      return 'amber';
    case 'failed':
      return 'red';
    case 'quarantined':
      return 'amber';
    default:
      return assertNever(v);
  }
}

// An unhandled verdict is a TYPE error at this line, not a grey badge on the
// worst row in the queue.
function assertNever(v: never): never {
  throw new Error(`unhandled verdict ${String(v)}`);
}

/**
 * The reviewer-facing NAME of a verdict — presentation only, decoupled from
 * the wire value.
 *
 * The wire value stays `quarantined` (it is the Verdict the server computes
 * and every Go test pins), but that word reads as "infected / dangerous" to
 * a reviewer when what it actually means is "the comparison could not run" —
 * a signal-killed capture, a geometry mismatch, no eligible reference. So the
 * UI says `not compared`, which is the same sentence the detail screen
 * already uses ("This flow was not compared"). The other three names are
 * unchanged.
 *
 * TOTAL like verdictTone, and for the same reason: a fifth verdict added to
 * the union is a type error here, not a raw `quarantined` leaking onto a
 * badge.
 */
export function verdictLabel(v: Item['verdict']): string {
  switch (v) {
    case 'pass':
      return 'pass';
    case 'changed':
      return 'changed';
    case 'failed':
      return 'failed';
    case 'quarantined':
      return 'not compared';
    default:
      return assertNever(v);
  }
}
