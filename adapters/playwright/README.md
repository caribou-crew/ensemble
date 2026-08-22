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

`checkpoint`/`group` names must match `^[A-Za-z0-9._-]+$` — the same rule
`retrace/runs/paths.go` enforces for on-disk path components — and throw
rather than silently skip a screenshot when violated.
