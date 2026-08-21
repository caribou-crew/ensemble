# Proposal: service-variants

Status: proposed (2026-08-21).

## Why

One logical service is sometimes backed by a 10 MB Go stub (connects to the
real DBs, implements the slice of the monolith a developer needs) and
sometimes by the 1.5 GB Java monolith itself. Today that means two service
blocks and commenting one out — `dir`/`build`/`run`/`env` differ, but the
port, proxy, health path, dependencies, and every consumer are identical.
The orchestrator already has the exact runtime primitive this needs:
`Flip` swaps a service between `run` and `docker` without touching the
proxy listener because both back the same port.

## What Changes

- A service MAY declare `variants:` — a map of named backings — plus
  `default:` naming the one `ensemble up` starts. Each variant carries the
  per-backing fields: `dir`, `build`, `watch`, `run`, `env`, `docker`,
  `startup_timeout_s`. Service-level fields (`port`, `proxy`, `health`,
  `depends_on`, `entry`, `profile`) are shared.
- Service-level `dir/build/run/...` remain valid as the single implicit
  variant; declaring both is a validation error. No migration.
- Runtime switch, generalizing `Flip`: `POST /api/services/{name}/variant`
  `{"variant": "real"}`, `ensemble variant <service> <variant>`, and a
  selector on the dashboard's service panel. Same port, so the proxy
  listener and any gateway route never notice. `Restart` keeps the current
  variant. A switch on a stopped service only records the choice.
- `ensemble up --variant svc=real[,svc2=x]` overrides `default` for one run.
- `ServiceState` gains `variant`; `GET /api/topology` nodes gain `variant`
  and `variants` so the UI can offer the choice.
- Build stamps become per-variant so switching never skips a stale build.

## Capabilities

### New Capabilities
- `service-variants`: named alternative backings for one service with a
  config default and a runtime switch over the existing Flip machinery.

### Modified Capabilities
<!-- Main specs are not synced yet; the Flip requirement's behaviour is
     unchanged — variants compose with placement rather than replacing it. -->

## Impact

- `ensemble/config`: `Variant` type, `Service.Variants/Default`,
  validation, `Config.ResolveService(name, variant)`.
- `ensemble/orchestrator`: per-service current-variant tracking,
  `SetVariant`, resolved service used by `startServiceAs`/`Restart`/`Flip`;
  `Opts.Variants`.
- `ensemble/server`: variant endpoint; `variant`/`variants` on topology nodes.
- `ensemble/cmd/ensemble`: `variant` command, `up --variant`, status column.
- `dashboard/ensemble-ui`: variant selector on the service panel.
- README: config reference.
