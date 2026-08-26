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

`"private": true` is deliberate, same as every other npm package in this
repo: it stays on until the maintainer syncs npm credentials and the
`npm-publish` job in `.github/workflows/release.yml` is enabled. See that
job's comment for the other half of the gate.
