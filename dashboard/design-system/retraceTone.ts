import type { Item } from './retraceTypes';

/**
 * Presentation only. There is deliberately NO copy of serve.ScoreOf here —
 * the server computes the score and this renders the number it sent.
 *
 * TOTAL, and enforced by the compiler: a tone function that can return
 * `undefined` is a zero-value hazard one level down (`<Badge tone={undefined}>`
 * renders NEUTRAL GREY, the colour of a non-event), so an unhandled fifth
 * verdict is a type error at this line, not a grey badge at a reviewer's
 * desk.
 *
 * `quarantined` is AMBER, not red or grey — "could not evaluate", not
 * "evaluated and bad".
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

function assertNever(v: never): never {
  throw new Error(`unhandled verdict ${String(v)}`);
}

/**
 * The reviewer-facing NAME of a verdict, decoupled from the wire value. The
 * wire value stays `quarantined` (every Go test pins it), but that word
 * reads as "infected / dangerous" when it actually means "the comparison
 * could not run" — so the UI says `not compared`. TOTAL like verdictTone.
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
