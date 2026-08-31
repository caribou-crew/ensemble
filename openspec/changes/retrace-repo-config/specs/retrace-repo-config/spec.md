## Purpose

Lets a repository declare, in one committed file, every retrace app it
owns and the directory each app's own `retrace.yaml`/`.retrace/` tree
already lives in — so standalone `retrace` tooling can find and aggregate
every app in the repo without depending on ensemble or a machine-global
config.

## ADDED Requirements

### Requirement: `retrace.repo.yaml` declares a repo's apps and their root directories
A `retrace.repo.yaml` file SHALL declare, under `apps:`, a map from app key
to a `root:` directory (relative to `retrace.repo.yaml`'s own location
unless absolute) — the directory holding that app's own `retrace.yaml` and
`.retrace/` tree. Two or more app keys MAY name the same root. An optional
top-level `repo:` (GitHub `org/repo`) and `sync:` block (workflows,
branch, actor, event, status, since) supply defaults for sync-capable
commands. An `apps:` map with no entries, or a file with no `apps:` key at
all, is a configuration error naming the file.

#### Scenario: A repo config maps several apps to the same root
- **WHEN** `retrace.repo.yaml` maps six app keys to `root: apps/sample/react-native`
- **THEN** loading the file succeeds and every one of the six app keys
  resolves to that same root directory

#### Scenario: A repo config maps apps across different roots
- **WHEN** `retrace.repo.yaml` maps `uxt-web` to `root: .` and six other
  app keys to `root: apps/sample/react-native`
- **THEN** loading the file succeeds and each app key resolves to its own
  mapped root

#### Scenario: A root path is relative to the config file's own location
- **WHEN** `retrace.repo.yaml` at the repo root maps an app to `root:
  apps/sample/react-native` and a command is invoked from a different
  working directory inside the repo
- **THEN** the app's root resolves to `<repo-root>/apps/sample/react-native`,
  not to a path relative to the command's own working directory

#### Scenario: An empty `apps:` map is a configuration error
- **WHEN** `retrace.repo.yaml` is present but its `apps:` key is missing or
  empty
- **THEN** loading the file fails with an error naming the file and stating
  that at least one app must be declared

### Requirement: Repo config is discovered by searching upward from the working directory
A command that supports repo-scoped operation SHALL search for
`retrace.repo.yaml` starting at the current working directory and then
each parent directory in turn, stopping at the first one found or at the
filesystem/repository root, whichever comes first. When no
`retrace.repo.yaml` is found above the working directory, the command
SHALL behave exactly as it does with no repo config at all — no error, no
behavior change.

#### Scenario: Found from a nested app directory
- **WHEN** `retrace.repo.yaml` exists at the repo root and a command runs
  from `apps/sample/react-native`, a subdirectory several levels below it
- **THEN** the command finds and loads the repo root's `retrace.repo.yaml`

#### Scenario: Found from the directory containing it
- **WHEN** a command runs from the same directory as `retrace.repo.yaml`
- **THEN** the command finds and loads it without searching any parent
  directory

#### Scenario: Not found anywhere above the working directory
- **WHEN** no `retrace.repo.yaml` exists in the working directory or any of
  its ancestors
- **THEN** the command proceeds with no repo config, identically to a
  repo that has never adopted this file

### Requirement: A malformed or invalid root is reported at load, not at first use
Loading `retrace.repo.yaml` SHALL validate that every mapped root resolves
to an existing directory, and SHALL fail with an error naming the
offending app key and root before any command proceeds — never silently
producing a partial app list.

#### Scenario: A mapped root does not exist
- **WHEN** `retrace.repo.yaml` maps an app key to a root directory that
  does not exist on disk
- **THEN** loading the file fails with an error naming that app key and
  root, before the command that requested the load does anything else
