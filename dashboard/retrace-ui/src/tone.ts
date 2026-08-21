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
