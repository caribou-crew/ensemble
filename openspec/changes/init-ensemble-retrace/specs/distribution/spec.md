# distribution

## ADDED Requirements

### Requirement: npm-wrapped binaries
`@caribou-crew/retrace` and `@caribou-crew/ensemble` SHALL install prebuilt
platform binaries via per-platform optionalDependencies with a bin shim (no
postinstall downloads), so `npm i -D` + `npx` is the complete setup for node
projects; adapters SHALL depend on the wrapper.

#### Scenario: Locked registry CI
- **WHEN** CI installs from a mirrored registry with no external network
- **THEN** `npx retrace replay` works with no additional downloads

### Requirement: Native installs
Releases SHALL also publish GitHub release archives, a Homebrew tap, and a
curl install script for darwin/linux/windows on arm64 and x64, built by
GoReleaser.

#### Scenario: Brew install
- **WHEN** a user runs `brew install <tap>/ensemble`
- **THEN** the `ensemble` binary with embedded dashboard is on PATH

### Requirement: Lockstep versioning
Each release SHALL use one version number across both binaries and all npm
packages, and npm wrappers SHALL pin their platform packages to that exact
version.

#### Scenario: No version skew
- **WHEN** version X of the wrapper installs
- **THEN** the delivered binary reports version X
