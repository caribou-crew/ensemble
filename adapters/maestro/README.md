# @caribou-crew/retrace-maestro

Flow-part markers for [Maestro](https://maestro.mobile.dev) mobile flows,
which cannot write files or import Node modules for assertions the way a
Playwright test can. Maestro flows call out via `runScript`, so this package
ships a plain executable that turns argv + env into one HTTP POST to
`RETRACE_MARKER_URL`.

## Usage

```yaml
- runScript:
    file: node_modules/@caribou-crew/retrace-maestro/bin/retrace-maestro.mjs
    env: { ARGS: "group checkout" }
# ... steps ...
- runScript:
    file: node_modules/@caribou-crew/retrace-maestro/bin/retrace-maestro.mjs
    env: { ARGS: "group --end" }
```

Outside a `retrace run` (`RETRACE_MARKER_URL` unset), the script is a silent
no-op. Set `RETRACE_STRICT=1` in the flow's environment to make that a loud
failure instead.

## API

`markerRequest(argv, env)` is the pure core — argv/env in, an HTTP request
description out, no network call — exported for anyone who wants to test or
embed the logic without invoking the CLI:

```ts
import { markerRequest } from '@caribou-crew/retrace-maestro';

markerRequest(['group', 'checkout'], process.env);
// → { url: 'http://127.0.0.1:PORT/group', body: '{"name":"checkout"}' }
markerRequest(['group', '--end'], process.env);
// → { url: 'http://127.0.0.1:PORT/group/end', body: '{}' }
```

`bin/retrace-maestro.mjs` is plain, unbuilt JavaScript — it is never a `tsc`
output, so a Maestro `runScript` step never depends on this package's own
build having run. Group names must match `^[A-Za-z0-9._-]+$`, same as
`@caribou-crew/retrace-js` and `@caribou-crew/retrace-playwright`.

## Publishing

`package.json` sets `"private": true` deliberately: it enforces "no
accidental `npm publish`" at the package level, on top of (not instead of)
`.github/workflows/release.yml`'s `npm-publish` job being `if: false`. Both
gates exist because the maintainer has not yet synced npm credentials for
`@caribou-crew`; publishing is theirs to authorize. When they do, clearing
`private` here is part of enabling it — see that job's comment for the
other half.
