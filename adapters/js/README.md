# @caribou-crew/retrace-js

The base test-runner adapter for [retrace](../../retrace). Marks flow-part
groups and reports where checkpoints belong, using the environment
`retrace run` injects into the test command:

| Variable             | Set by `retrace run` for                          |
| --------------------- | -------------------------------------------------- |
| `RETRACE_RUN_DIR`     | file-writing adapters (this package, checkpoints)   |
| `RETRACE_PROXY_URL`   | pointing the app under test at retrace's proxy      |
| `RETRACE_MARKER_URL`  | HTTP-only runners that cannot write files (Maestro) |
| `RETRACE_STRICT`      | `1`/`true`/`yes`/`on` = fail loudly outside a run    |

Zero runtime dependencies: `node:fs`, `node:path`, and global `fetch`.

## Usage

```ts
import { group, endGroup } from '@caribou-crew/retrace-js';

await group('checkout');
// ...exercise the checkout flow...
await endGroup();
```

Outside a `retrace run` (plain `vitest`/`jest`/CI without retrace), `group`
and `endGroup` are no-ops — your suite runs unmodified. Set
`RETRACE_STRICT=1` to make a missing handshake a loud error instead, per the
spec's "fail loudly" requirement:

```ts
import { requireHandshake } from '@caribou-crew/retrace-js';

requireHandshake(); // throws MISSING_HANDSHAKE_MESSAGE if RETRACE_STRICT=1
                     // and neither RETRACE_RUN_DIR nor RETRACE_MARKER_URL is set
```

## API

- `handshake(env?)` — reads the four env vars into a `Handshake`. Never
  throws for a missing run; throws if `RETRACE_STRICT` holds a value outside
  `1/true/yes/on/0/false/no/off` (case-insensitive) or unset/empty.
- `requireHandshake(env?)` — `handshake()` plus: throws
  `MISSING_HANDSHAKE_MESSAGE` when strict mode is on and there is no run to
  write to.
- `group(name, options?)` / `endGroup()` — append a flow-part marker, to
  `groups.jsonl` in `RETRACE_RUN_DIR` when set, else POSTed to
  `RETRACE_MARKER_URL`. `name` must match `^[A-Za-z0-9._-]+$` (mirrors
  `retrace/runs/paths.go`'s path-component guard) — a name outside that set
  throws rather than silently writing nowhere.
- `shotsDir(env?)` — the directory checkpoints belong in
  (`RETRACE_RUN_DIR/shots`), or `null` outside a run. Used by
  `@caribou-crew/retrace-playwright`.
- `MISSING_HANDSHAKE_MESSAGE` — the shared strict-mode error text, imported
  verbatim by `@caribou-crew/retrace-playwright` and
  `@caribou-crew/retrace-maestro`.
