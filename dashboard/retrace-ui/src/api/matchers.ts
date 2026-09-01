/**
 * The wire-rule matcher dialect, as the picker's option list — F6.
 *
 * `rules.ParseMatcher` accepts `exact`, `ignore`, six named matchers, or a
 * `{pattern: …}` object, and `config.AppendWireRule` validates BEFORE it
 * writes. So a matcher field that is a free-text box is a control on which
 * every typo — and the shipped default, `any`, which is not a member of the
 * dialect at all — makes the rule verb answer 400. The verb was broken on its
 * own default path.
 *
 * A closed set gets a `<select>`: it cannot be typo'd, it cannot default to a
 * non-member, and it makes the eight options discoverable, which a text box
 * with a placeholder does not.
 *
 * ONE HOME, MECHANICALLY CHECKED. The list below is a copy of `rules.Names()`
 * — Go decides the dialect and this is a transcript of it — so
 * `TestTheReviewUIsMatcherOptionsAreExactlyTheDialect` in retrace/rules reads
 * THIS FILE and asserts the two are equal, in order. Add a matcher in Go
 * without adding it here (or reorder either) and that test goes red. Keep the
 * literal below a plain array of single-quoted strings on their own lines;
 * that is the shape the Go test parses.
 */
export const MATCHER_NAMES = [
  'exact',
  'ignore',
  'etag',
  'http-date',
  'integer',
  'iso8601',
  'redacted',
  'semver',
  'uuid',
] as const;

export type MatcherName = (typeof MATCHER_NAMES)[number];

/**
 * The matcher the picker opens on.
 *
 * `exact` — the STRICTEST member, and deliberately: a rule written without
 * touching this control tolerates nothing, which is a no-op the reviewer can
 * see and undo. The alternative default, `ignore`, would silence a field in
 * every flow in the project on a control the reviewer never touched, and the
 * old default (`any`) was not a matcher at all, so every unedited rule 400'd.
 */
export const DEFAULT_MATCHER: MatcherName = 'exact';
