## ADDED Requirements

### Requirement: `retrace sync` can be scoped to an allowlist of app keys
`retrace sync`'s merge step SHALL accept an optional allowlist of app
keys. When set, a downloaded artifact's `<app>/<flow>/<run-id>/` run
directory SHALL be merged into `.retrace/runs/` only when `<app>` is in
the allowlist; a run directory for an app not in the allowlist SHALL be
reported in the result as skipped, with a reason distinguishing it from a
malformed-artifact skip, and SHALL NOT be written anywhere. When no
allowlist is set, every run directory an artifact contains SHALL be merged
exactly as it is today (unchanged from the behavior `retrace-sync`
already specifies).

#### Scenario: Allowlist admits only the named apps
- **WHEN** `retrace sync` runs with an allowlist of `["uxt-rn-ios"]` against
  an artifact containing run directories for both `uxt-rn-ios` and
  `uxt-web`
- **THEN** only the `uxt-rn-ios` run directory is merged into
  `.retrace/runs/`, and the `uxt-web` run directory is reported as skipped
  with a reason naming the allowlist

#### Scenario: No allowlist merges everything, unchanged
- **WHEN** `retrace sync` runs with no allowlist set, against the same
  artifact as above
- **THEN** both the `uxt-rn-ios` and `uxt-web` run directories are merged,
  identically to `retrace sync`'s behavior before this requirement existed
