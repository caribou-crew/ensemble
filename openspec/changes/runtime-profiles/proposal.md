# Proposal: runtime-profiles

Status: proposed (2026-08-21).

## Why

A stack often has a couple of verticals ("lanes"); most of the time a
developer needs one, occasionally both, and the second is memory-hungry.
`profiles:` already expresses the grouping, but only at `ensemble up
--profile` time: adding the second lane, or freeing its memory, means
tearing the whole stack down and starting over. Profiles need to be
switchable while the stack runs.

## What Changes

- The orchestrator's active profile set becomes runtime state:
  `UpProfiles(names)` starts every service the newly-active set adds that
  isn't already running, in dependency order, wiring its proxy listener
  if it has none yet; `DownProfiles(names)` stops every running service the
  remaining set no longer covers, dependents first. A service is covered
  while **any** active profile (or no profile at all — the always-on spine)
  names it, so a shared service stays up as long as one lane needs it.
  Databases are always-on and never touched.
- `GET /api/profiles` reports every profile (members, active flag);
  `POST /api/profiles/{name}/up|down` switches one.
- CLI: `ensemble up lane2` while a stack is running adds the lane to it
  (otherwise starts the stack with that lane active — same as
  `--profile lane2`); `ensemble down lane2` drops the lane; bare
  `ensemble down` still shuts everything down; `ensemble profiles` lists.
- Dashboard: a profile toggle strip on the topology view.
- No config changes. Configs without profiles are unaffected.

## Capabilities

### New Capabilities
- `runtime-profiles`: activate and deactivate configured profiles on a
  running stack with reference-counted service lifecycle.

## Impact

- `ensemble/config`: `ProfileNames()`, `ProfileMembers(name)`.
- `ensemble/orchestrator`: active set as state; `UpProfiles`,
  `DownProfiles`, `Profiles()`; idempotent `wireProxy`; `topoOrder` over an
  explicit set.
- `ensemble/server`: three routes.
- `ensemble/cmd/ensemble`: `up`/`down` positional profiles, `profiles`
  command, client methods.
- `dashboard/ensemble-ui`: profile strip.
- README: "Profiles as lanes" section.
