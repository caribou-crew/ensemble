# @caribou-crew/retrace

Installs the prebuilt `retrace` binary via a platform-specific
`optionalDependency` and exposes it as `retrace` on `PATH` — `npm i -D
@caribou-crew/retrace` + `npx retrace` is the whole setup, no Go toolchain
required.

## Install

```
npm i -D @caribou-crew/retrace
```

## Use

Wrap your existing test command — retrace sets up the environment handshake
its adapters (e.g. `@caribou-crew/retrace-playwright`) read to record
checkpoints:

```
npx retrace run -- npx playwright test
```

## Supported platforms

darwin (arm64, x64) and linux (arm64, x64) — the complete
`optionalDependencies` list below. Windows isn't published here yet; build
from source in the meantime (`go run ./retrace/cmd/retrace` from a checkout
of [caribou-crew/ensemble](https://github.com/caribou-crew/ensemble)).

## Publishing

Published via GitHub Actions' `publish.yml` workflow using npm's [trusted
publishing](https://docs.npmjs.com/trusted-publishers/) (OIDC) — no
long-lived npm token stored in this repo. See that workflow's comments for
the one-time per-package setup a maintainer with publish rights on
`@caribou-crew` has to do on npmjs.com before the first release.
