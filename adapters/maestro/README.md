# @caribou-crew/retrace-maestro

Flow-part markers for [Maestro](https://maestro.mobile.dev) mobile flows,
using its native JavaScript `runScript` command. The flow script reads
Maestro variables and uses its built-in HTTP client to POST markers to
`RETRACE_MARKER_URL`. A separate Node CLI supports host-side marker commands
and file evidence.

## Usage

Pass retrace's handshake into Maestro explicitly. Maestro automatically
imports shell variables with a `MAESTRO_` prefix; retrace's variables need
`-e`. The inner shell expands them after `retrace run` sets them:

```sh
retrace run --flow checkout -- sh -c 'maestro test \
  -e RETRACE_MARKER_URL="$RETRACE_MARKER_URL" \
  -e RETRACE_STRICT=1 flows/checkout.yaml'
```

For a flow at `flows/checkout.yaml`, script paths are relative to that file:

```yaml
- runScript:
    file: ../node_modules/@caribou-crew/retrace-maestro/bin/retrace-maestro.js
    env: { ARGS: "group checkout" }
# ... steps ...
- runScript:
    file: ../node_modules/@caribou-crew/retrace-maestro/bin/retrace-maestro.js
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

`bin/retrace-maestro.js` runs in both Maestro's Rhino and GraalJS engines.
It uses ES5 syntax and needs neither Node nor a package build. Group names
must be non-empty, not start with `.`, and match `^[A-Za-z0-9._-]+$`, same
as `@caribou-crew/retrace-js` and `@caribou-crew/retrace-playwright`.

The existing `retrace-maestro` executable and `bin/retrace-maestro.mjs`
remain Node entrypoints. Use them in a host shell for `group checkout`,
`group --end`, `attach video <path>`, or `attach report <directory>`.
File evidence commands require `RETRACE_RUN_DIR` in that shell.

See Maestro's [runScript reference](https://docs.maestro.dev/api-reference/commands/runscript),
[parameters guide](https://docs.maestro.dev/maestro-flows/flow-control-and-logic/parameters-and-constants),
and [HTTP client guide](https://docs.maestro.dev/advanced/javascript/make-http-s-requests).

## Testing

`pnpm test` covers the native script in an isolated JavaScript context with
flow globals, plus the existing Node CLI. With Maestro and Java installed,
also run the actual Rhino/GraalJS engines against a local HTTP marker endpoint:

```sh
MAESTRO_TEST_CLASSPATH="$HOME/.maestro/lib/*" pnpm test
```

The real-engine checks are skipped when `MAESTRO_TEST_CLASSPATH` is unset;
they do not launch a simulator or emulator.

## Publishing

`package.json` sets `"private": true` deliberately: it enforces "no
accidental `npm publish`" at the package level, on top of (not instead of)
npm's [trusted publishing](https://docs.npmjs.com/trusted-publishers/)
being set up for this package on npmjs.com — see
`.github/workflows/publish.yml`'s comments for what that setup involves.
Publishing this package is the maintainer's call to make; when they do,
clearing `private` here is part of enabling it.
