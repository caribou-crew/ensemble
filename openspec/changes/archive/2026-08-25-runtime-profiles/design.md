# Design: runtime-profiles

## Context

`Opts.Profiles` is read through `cfg.ServicesForProfiles` in `Up`,
`Restart`, `Flip`, and `topoOrder`. `wireProxy` binds once per service from
`Up` and has no idempotence guard. `Down` tears down everything tracked.
Per-service teardown now exists as `stopCurrent` (service-variants).

## Goals / Non-Goals

**Goals:** switch profiles on a running stack from REST/CLI/dashboard;
never stop a service another active lane (or the always-on set) needs;
reuse `Up`'s start path and ordering.

**Non-Goals:** starting/stopping databases per lane (always-on); per-lane
variant defaults; a TUI.

## Decisions

### D1. Active set lives on the orchestrator, every reader goes through it

`active map[string]bool` seeded from `Opts.Profiles`; `activeServices()`
= `cfg.ServicesForProfiles(sorted keys)`. `Up/Restart/Flip/topoOrder` call
it. `topoOrder(set)` takes the service set explicitly so Down can order
over all services.

### D2. UpProfiles = Up restricted to what isn't running

Add names to `active` (unknown profile → error before any change), then
walk `topoOrder(activeServices())` and, for each service/database with no
tracked process/container, start it exactly as `Up` does (resolve variant,
`startService`, `wireProxy`). `wireProxy` gains a `wired` set so a service
that was up before and is being re-added doesn't try to rebind.

### D3. DownProfiles = stop what the remaining set no longer covers

Remove names from `active`, compute `activeServices()`, then walk the
reverse of `topoOrder(all services)` and `stopCurrent` + `StatusStopped`
every tracked service not in the new set. Refcounting falls out of
`ServicesForProfiles`' "any active profile" rule — nothing to count.
Databases are skipped (always-on).

### D4. CLI ergonomics

`ensemble up [flags] [profile…]`: positional profiles. If a control plane
answers `GET /api/health` at the client URL (`ENSEMBLE_API` /
`127.0.0.1:4700`), the profiles are added to it and the command returns;
otherwise they become the run's initial active set and `up` starts the
stack as usual. `ensemble down [profile…]` with profiles deactivates them
(needs a running stack); without, it is the existing full shutdown.
`ensemble profiles` lists. The attach-vs-start fork is printed so it's
never ambiguous which happened.

### D5. Surfaces

`ProfilesState{Active []string, Profiles []ProfileInfo{Name, Services,
Active}}` from `GET /api/profiles` and returned by both POSTs.

## Risks / Trade-offs

- [`ensemble up lane1` attaches to whatever stack is listening, which may
  be another project's] → The attach message names the URL; `ENSEMBLE_API`
  scopes it.
- [A lane service in `failed` state is still "tracked" and won't be
  restarted by `UpProfiles`] → Same as `Up`; `Restart` exists for that.
