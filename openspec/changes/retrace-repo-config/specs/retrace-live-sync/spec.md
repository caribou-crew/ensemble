## Purpose

Turns `retrace serve` from a static viewer into a hands-off standalone
dashboard by pulling new GitHub Actions runs on a recurring interval,
scoped correctly per root, without a developer re-running `retrace sync`
by hand.

## ADDED Requirements

### Requirement: `retrace serve --watch` periodically syncs every mapped root
`retrace serve --watch` SHALL, in addition to serving the review queue,
periodically perform the same run-pulling sync `retrace sync` performs —
once for each distinct root a `retrace.repo.yaml` maps — using that repo
config's `sync:` block (or equivalent CLI flags, which take precedence)
for the repo, workflow, branch, and lookback filters. The first sync SHALL
run once immediately at startup, not only after the first interval
elapses. `--watch` without a discoverable repo config SHALL sync the
single working-directory root the same way, on the same interval.

#### Scenario: New CI run appears without a manual sync
- **WHEN** `retrace serve --watch` is running against a repo config with a
  configured `repo:`, and a new qualifying GitHub Actions run completes
  after the server started
- **THEN** within one sync interval, that run's artifact is pulled and
  merged into the correct root's `.retrace/runs/` tree with no additional
  command run by the developer

#### Scenario: --watch with no repo config still syncs the single root
- **WHEN** `retrace serve --watch --repo org/repo` runs with no
  `retrace.repo.yaml` found above the working directory
- **THEN** the working directory's own `.retrace/runs/` tree is synced on
  the same interval, exactly as `retrace sync` would if run by hand
  repeatedly

### Requirement: A sync failure does not stop the server
An error from one root's sync attempt (a `gh` auth failure, a GitHub API
error, a network failure) SHALL be reported (to the process's error
output) and SHALL NOT stop the HTTP server, the watch loop, or any other
root's sync on the same or a later tick.

#### Scenario: gh auth expires mid-session
- **WHEN** `retrace serve --watch` is running and a scheduled sync tick
  fails because `gh`'s credentials have expired
- **THEN** the failure is reported, the dashboard continues serving its
  last-known state, and the next scheduled tick still attempts a sync

### Requirement: A sync scoped to one root only merges that root's own apps
When `retrace serve --watch` syncs a root that a repo config maps to a
specific set of app keys, the sync SHALL merge only run directories for
apps in that root's set, even when a downloaded artifact also contains run
directories for apps mapped to a different root. Apps excluded this way
SHALL be reported the same way a malformed artifact is reported today —
named, with a reason — never silently dropped and never merged into the
wrong root.

#### Scenario: One GitHub repo backs two roots with different apps
- **WHEN** `retrace.repo.yaml` maps `uxt-web` to root `.` and `uxt-rn-ios`
  to root `apps/sample/react-native`, both under the same `repo:`, and a
  sync tick for the `apps/sample/react-native` root processes an artifact
  that contains run directories for both `uxt-web` and `uxt-rn-ios`
- **THEN** only the `uxt-rn-ios` run directory is merged into
  `apps/sample/react-native/.retrace/runs/`, and the `uxt-web` run
  directory is reported as skipped for that root, not merged there
