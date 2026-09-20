# Portable review workspace browser tests

These tests load the production Vite build, with synthetic suite reports, saved
pair responses and SVG images intercepted at the browser HTTP boundary. They
require no Go server, Taxi data, credentials, local recordings or remote API.
They test browser behavior rather than backend comparison correctness or visual
pixel parity. Go suites cover the backend separately.

From the repository root:

```sh
pnpm install --frozen-lockfile
pnpm --filter retrace-ui exec playwright install chromium
pnpm --filter retrace-ui test:e2e
```

Linux hosts may need `playwright install --with-deps chromium`. To use an already
installed Chrome locally: `PLAYWRIGHT_CHANNEL=chrome pnpm --filter retrace-ui test:e2e`.
Port 4971 must be available; the suite refuses to reuse an unrelated server.

The suite verifies originals and pinned URLs, next/previous navigation, diff/wire
switching, full-comparison return and reload, status/search persistence, keyboard
focus and real key input, absent evidence/screenshots, HTTP image failure and
recovery, and a 600px layout without horizontal overflow. Unknown API routes,
methods and external origins fail the test; images accept only known pair, flow,
checkpoint and side identifiers.

`retrace-browser` in CI installs Chromium, builds the UI and runs these tests.
Failure artifacts (HTML report, traces and screenshots) are retained for seven
days. Unit Vitest excludes this directory; the browser script typechecks its own
fixtures and config. Browser tests have no automatic retries to conceal flakes.

Initial local validation: six browser scenarios pass against installed Chrome.
The full JavaScript and Go suites also pass (existing Maestro skips remain).
Removing URL-filter persistence, image-error handling, or the typing shortcut
guard causes the corresponding browser scenario to fail. The first missing-image run found a real UI defect:
broken assets now have an explicit unavailable explanation and a direct-image link.

CI configuration is checked in; a hosted GitHub run has not been dispatched by
this local task.
