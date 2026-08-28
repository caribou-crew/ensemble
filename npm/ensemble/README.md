# @caribou-crew/ensemble

Installs the prebuilt `ensemble` binary via a platform-specific
`optionalDependency` and exposes it as `ensemble` on `PATH` — `npm i -g
@caribou-crew/ensemble` and you have `ensemble up`, no Go toolchain
required.

## Install

```
npm i -g @caribou-crew/ensemble
```

## Use

```
ensemble up
```

Runs the stack described by `ensemble.yaml` in the current directory —
services, databases, stubs, gateways — and serves the dashboard at
`http://127.0.0.1:4700`.

## Supported platforms

darwin (arm64, x64) and linux (arm64, x64) — the complete
`optionalDependencies` list below. Windows isn't published here yet; build
from source in the meantime (`go run ./ensemble/cmd/ensemble` from a
checkout of [caribou-crew/ensemble](https://github.com/caribou-crew/ensemble)).

## Publishing

Published via GitHub Actions' `publish.yml` workflow using npm's [trusted
publishing](https://docs.npmjs.com/trusted-publishers/) (OIDC) — no
long-lived npm token stored in this repo. See that workflow's comments for
the one-time per-package setup a maintainer with publish rights on
`@caribou-crew` has to do on npmjs.com before the first release.
