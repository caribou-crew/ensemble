# @caribou-crew/retrace-playwright

A Playwright fixture that turns `retrace run`'s environment into checkpoints
and flow-part markers. Builds on
[`@caribou-crew/retrace-js`](../js) for the env handshake and
`group`/`endGroup`.

`@playwright/test` is a **peerDependency**, not a dependency — you own your
Playwright version.

## Usage

```ts
import { test } from '@caribou-crew/retrace-playwright';

test('checkout', async ({ page, retrace }) => {
  await page.goto('/cart');
  await retrace.group('checkout');
  await retrace.checkpoint('cart');
  await page.getByRole('button', { name: 'Buy now' }).click();
  await retrace.checkpoint('confirmation', { selector: '#receipt', trim: true });
  await retrace.endGroup();
});
```

- `checkpoint(name, options?)` — screenshots the page (or `options.selector`,
  a CSS selector string or an already-scoped `Locator` — useful for
  cross-origin frames) into `RETRACE_RUN_DIR/shots/<name>.png`. `trim: true`
  writes an empty `<name>.trim` marker beside the shot; the Go binary
  (`retrace/capture`, `retrace/pixel`) owns the actual uniform-border
  cropping at compare time, so this package never duplicates that pixel
  work.
- `group(name)` / `endGroup()` — delegate straight to
  `@caribou-crew/retrace-js`.

Outside a `retrace run`, `checkpoint`/`group`/`endGroup` are no-ops and your
suite runs unmodified. Set `RETRACE_STRICT=1` (see the base package's README)
to make a missing handshake fail loudly instead — including when a
checkpoint has nowhere to write because only `RETRACE_MARKER_URL` (no
filesystem) is available, since screenshots are file-only.

`checkpoint`/`group` names must be non-empty, not start with `.`, and match
`^[A-Za-z0-9._-]+$` — reproducing the full guard
`retrace/runs.ValidateComponents` enforces for on-disk path components, not
just its regex — and throw rather than silently skip a screenshot when
violated.

## Publishing

`package.json` sets `"private": true` deliberately: it enforces "no
accidental `npm publish`" at the package level, on top of (not instead of)
npm's [trusted publishing](https://docs.npmjs.com/trusted-publishers/)
being set up for this package on npmjs.com — see
`.github/workflows/publish.yml`'s comments for what that setup involves.
Publishing this package is the maintainer's call to make; when they do,
clearing `private` here is part of enabling it.
