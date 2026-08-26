// The keyboard map for the review screen, as a pure function of the four
// fields of a KeyboardEvent this app actually consults. Pure, and taking a
// structural shape rather than a KeyboardEvent, so the map is testable
// without a DOM event and so App's handler contains no policy of its own.

export type Action =
  | 'next'
  | 'prev'
  | 'open'
  | 'accept'
  | 'reject'
  | 'rule'
  | 'help'
  | 'back';

/** The shape actionFor reads off a KeyboardEvent. */
export interface KeyEventLike {
  key: string;
  ctrlKey: boolean;
  metaKey: boolean;
  altKey: boolean;
  target: EventTarget | null;
}

// One table, so the help overlay and the dispatcher cannot disagree about
// what a key does — the help sheet is rendered FROM this, not written
// beside it.
const MAP: Record<string, Action> = {
  j: 'next',
  ArrowDown: 'next',
  k: 'prev',
  ArrowUp: 'prev',
  Enter: 'open',
  Escape: 'back',
  a: 'accept',
  r: 'reject',
  u: 'rule',
  '?': 'help',
};

/** The keys, in the order the help sheet lists them. */
export const KEY_HELP: { keys: string; what: string }[] = [
  { keys: 'j / ↓', what: 'next flow' },
  { keys: 'k / ↑', what: 'previous flow' },
  { keys: 'enter', what: 'open the selected flow' },
  { keys: 'esc', what: 'back to the queue' },
  { keys: 'a', what: 'accept this run as the new reference' },
  { keys: 'r', what: 'reject it and write a repro bundle' },
  { keys: 'u', what: 'write a wire rule for the selected field' },
  { keys: '?', what: 'this help' },
];

// A keystroke aimed at a text field belongs to the text field. Without
// this, typing "a" into the matcher box of the rule picker would ACCEPT the
// run under review — a filesystem mutation triggered by a letter the user
// was typing into an input.
function isTyping(target: EventTarget | null): boolean {
  if (target === null || typeof target !== 'object') return false;
  const el = target as Partial<HTMLElement> & { tagName?: string };
  if (el.isContentEditable === true) return true;
  const tag = typeof el.tagName === 'string' ? el.tagName.toUpperCase() : '';
  return tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT';
}

/**
 * The action a keystroke means, or null when it means nothing to this app.
 *
 * null — not a guess — is the answer for anything unmapped, for anything a
 * modifier is held for (⌘R must reload the page, not reject the run), and
 * for anything typed into a field.
 */
export function actionFor(e: KeyEventLike): Action | null {
  if (e.ctrlKey || e.metaKey || e.altKey) return null;
  if (isTyping(e.target)) return null;
  return MAP[e.key] ?? null;
}
