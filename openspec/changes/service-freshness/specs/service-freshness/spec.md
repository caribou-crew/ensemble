## ADDED Requirements

### Requirement: Freshness configuration
A config MAY declare a top-level `freshness:` block with `default_branch`
(string, defaults to `main` when the block is present but the field is
omitted) and `poll_interval_s` (integer seconds, defaults to `300`).
Absence of the `freshness:` block SHALL disable the background freshness
checker entirely — no fetches run, and no service's state carries a
`Freshness` field.

#### Scenario: Minimal freshness config
- **WHEN** `ensemble.yaml` declares `freshness: {}`
- **THEN** the checker runs with `default_branch: main` and
  `poll_interval_s: 300`

#### Scenario: No freshness config
- **WHEN** `ensemble.yaml` has no `freshness:` key
- **THEN** the orchestrator starts no background fetch goroutine and every
  `ServiceState.Freshness` is omitted from `GET /api/status`

### Requirement: Service eligibility
A service SHALL be checked for freshness only if its resolved `Dir` (or the
`Dir` of its active variant) is inside a git working tree, and that
repository's toplevel differs from the toplevel of the directory
containing `ensemble.yaml`. Services that fail either condition SHALL be
skipped silently — no error, no state entry.

#### Scenario: Independent service repo
- **WHEN** a service's `Dir` resolves to a git repo whose toplevel is not
  the ensemble.yaml repo's toplevel
- **THEN** the service is included in the freshness poll

#### Scenario: Stub living in the config repo
- **WHEN** a service's `Dir` is inside the same git repo as `ensemble.yaml`
- **THEN** the service is skipped and its `ServiceState.Freshness` is
  omitted

#### Scenario: Dir is not a git repo
- **WHEN** a service's `Dir` is not inside any git working tree
- **THEN** the service is skipped and its `ServiceState.Freshness` is
  omitted

### Requirement: Background freshness polling
While the orchestrator is up, a background process SHALL, once every
`poll_interval_s`, run `git fetch origin` against each eligible service's
`Dir` (bounded concurrency, bounded per-fetch timeout matching the existing
version-fingerprint timeout) and then compute how many commits the
service's `HEAD` is behind `origin/<current-branch>` and behind
`origin/<default_branch>` via `git rev-list --count`. The process SHALL
start when the orchestrator starts (`Up`) and stop when it stops (`Down`),
and SHALL never block `Up`, `Down`, or any request-serving path on a fetch
completing.

#### Scenario: Poll runs on schedule
- **WHEN** the orchestrator has been up longer than one `poll_interval_s`
- **THEN** at least one freshness check has completed for every eligible
  service

#### Scenario: Fetch does not block startup
- **WHEN** `ensemble up` is invoked with `freshness:` configured
- **THEN** services report `Running` (health-gated as usual) without
  waiting for any freshness fetch to complete

#### Scenario: Orchestrator shutdown stops polling
- **WHEN** `Down` is called
- **THEN** the background freshness goroutine exits and issues no further
  fetches

### Requirement: Freshness state shape
Each eligible service's state SHALL carry a `Freshness` object with:
`branch` (current branch name via `symbolic-ref --short HEAD`),
`behindBranch` (int, commits behind `origin/<branch>`), `behindDefault`
(int, commits behind `origin/<default_branch>`), `defaultBranch` (string,
echoing the configured value), `checkedAt` (RFC3339 timestamp of the last
successful fetch, empty if never successful), and `error` (string,
non-empty when the most recent fetch attempt failed). A successful check
SHALL update `checkedAt`, `behindBranch`, and `behindDefault` together and
clear `error`. A failed check SHALL set `error` and leave `checkedAt`,
`behindBranch`, and `behindDefault` at their prior values.

#### Scenario: Up to date
- **WHEN** a service's `HEAD` has no commits behind either its remote
  branch or the default branch
- **THEN** `behindBranch` and `behindDefault` are both `0` and `error` is
  empty

#### Scenario: Behind own branch
- **WHEN** `origin/<branch>` has 3 commits not on `HEAD`
- **THEN** `behindBranch` is `3`

#### Scenario: Behind default branch
- **WHEN** `origin/<default_branch>` has 7 commits not reachable from
  `HEAD`
- **THEN** `behindDefault` is `7`

#### Scenario: Fetch failure preserves last-known state
- **WHEN** a fetch fails (network unreachable, auth failure) after a prior
  successful check reported `behindBranch: 2`
- **THEN** the next state reports `error` non-empty, `behindBranch` still
  `2`, and `checkedAt` unchanged from the prior successful check

#### Scenario: Never successfully checked
- **WHEN** a service has been eligible since startup but every fetch so far
  has failed
- **THEN** `checkedAt` is empty and `error` is non-empty

### Requirement: On-demand recheck
The server SHALL expose `POST /api/freshness/check` which triggers an
immediate freshness pass for every eligible service outside the normal
poll schedule, without waiting for the current `poll_interval_s` to elapse,
and returns once the pass completes.

#### Scenario: Manual refresh
- **WHEN** `POST /api/freshness/check` is called
- **THEN** every eligible service's `Freshness.checkedAt` reflects a fetch
  performed after the request was received

### Requirement: Status API exposure
`GET /api/status` SHALL include each eligible service's `Freshness` object
under its `ServiceState` entry. Services with no `Freshness` (disabled
config, ineligible service) SHALL omit the field entirely rather than
emit `null` or zero-valued placeholders.

#### Scenario: Freshness present in status
- **WHEN** freshness is configured and a service has been checked
- **THEN** `GET /api/status` includes `services[].freshness.branch`,
  `.behindBranch`, `.behindDefault`, `.defaultBranch`, `.checkedAt`, and
  `.error` for that service

#### Scenario: Freshness absent when disabled
- **WHEN** no `freshness:` config is present
- **THEN** no service entry in `GET /api/status` has a `freshness` key

### Requirement: CLI exposure
`ensemble status --json` SHALL include the same `freshness` field via the
existing `ServiceState` payload. `ensemble freshness` SHALL print a table
of service, branch, behind-count, behind-default-count, and last-checked
time for every eligible service, sourced from the orchestrator's current
state without triggering a new fetch.

#### Scenario: JSON status includes freshness
- **WHEN** `ensemble status --json` is run against a stack with freshness
  configured
- **THEN** the output's service entries include the `freshness` field

#### Scenario: Freshness table command
- **WHEN** `ensemble freshness` is run
- **THEN** it prints one row per eligible service with branch, behind
  counts, and last-checked time, and no row for skipped/ineligible
  services

### Requirement: Dashboard freshness indicator
The Services tab SHALL render a per-service freshness indicator derived
from `ServiceState.Freshness`: no badge when `behindBranch` and
`behindDefault` are both `0` and `checkedAt` is non-empty; an amber badge
showing `behindBranch` when it is greater than `0`; an amber badge showing
`behindDefault` against the configured default branch name when it is
greater than `0` (both badges render together when both are non-zero); and
a grey/unknown indicator when `checkedAt` is empty (never successfully
checked) or `error` is non-empty. The dashboard SHALL provide a control
that calls `POST /api/freshness/check` and refreshes the displayed state.

#### Scenario: Clean service shows no badge
- **WHEN** a service's freshness state has `behindBranch: 0`,
  `behindDefault: 0`, and a non-empty `checkedAt`
- **THEN** the Services tab renders no freshness badge for that row

#### Scenario: Behind badge
- **WHEN** a service's freshness state has `behindBranch: 3`
- **THEN** the Services tab renders an amber badge reading the behind-count

#### Scenario: Unknown state renders distinctly from clean
- **WHEN** a service's freshness state has empty `checkedAt` or non-empty
  `error`
- **THEN** the Services tab renders a grey/unknown indicator, never the
  no-badge "clean" state
