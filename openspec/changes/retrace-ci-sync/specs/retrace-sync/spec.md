## ADDED Requirements

### Requirement: `retrace sync` pulls GitHub Actions artifacts into `.retrace/runs`
`retrace sync --from github --repo <org/repo>` SHALL list recent
workflow-run artifacts for the given repo (optionally filtered to one
`--workflow`, and to runs newer than `--since`, default `7d`), download
each via the `gh` CLI, and merge every run directory an artifact contains
(`<app>/<flow>/<run-id>/` with a `manifest.json`) into the local
`.retrace/runs/` tree at the same path. An artifact whose contents do not
contain at least one `<app>/<flow>/<run-id>/manifest.json` SHALL be
reported and skipped, never partially merged.

#### Scenario: First sync pulls everything in range
- **WHEN** `retrace sync --from github --repo org/repo` runs against a repo
  with three qualifying workflow-run artifacts, each containing one run
  directory, and none of those run-ids exist locally yet
- **THEN** all three run directories are copied into `.retrace/runs/`, and
  the command reports three runs synced

#### Scenario: Re-running sync is idempotent
- **WHEN** `retrace sync` runs a second time against the same repo with no
  new qualifying workflow runs
- **THEN** every candidate run-id directory already exists locally, no
  files are re-downloaded or overwritten, and the command reports zero new
  runs synced

#### Scenario: Malformed artifact is skipped, not merged
- **WHEN** a downloaded artifact contains files but no `<app>/<flow>/
  <run-id>/manifest.json` anywhere in it
- **THEN** that artifact is reported by name as skipped and nothing from it
  is written under `.retrace/runs/`

#### Scenario: `gh` is missing
- **WHEN** `retrace sync --from github` runs and the `gh` binary is not on
  `PATH`
- **THEN** the command exits with an error naming `gh` and pointing at `gh
  auth login`, before attempting any network call

### Requirement: Synced runs carry CI provenance
Every run directory merged in by `retrace sync` SHALL get a
`source.json` sidecar file beside its `manifest.json` recording `kind:
"ci"`, the source workflow name, the GitHub Actions run URL, the commit
SHA the run was recorded against, and the time the sync happened. A run
directory with no `source.json` SHALL be treated as locally recorded. This
sidecar SHALL NOT modify `manifest.json` or any field `retrace/diff`
reads to compute a verdict.

#### Scenario: Synced run is marked CI
- **WHEN** `retrace sync` merges in a run directory from a GitHub Actions
  artifact
- **THEN** that run directory contains a `source.json` with `kind: "ci"`,
  the run's workflow name, and its GitHub Actions run URL

#### Scenario: Locally recorded run has no sidecar
- **WHEN** `retrace run` records a run the ordinary way
- **THEN** no `source.json` is written for that run, and any reader treats
  its absence as "recorded locally"

#### Scenario: Provenance does not affect the verdict
- **WHEN** two runs — one local, one synced from CI — are diffed against
  the same reference
- **THEN** `retrace diff`'s verdict, counts, and gates for each are
  computed identically to a run with no `source.json` at all
