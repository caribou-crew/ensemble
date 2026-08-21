# Design: service-variants

## Context

`config.Service` carries both identity (`port`, `proxy`, `health`,
`depends_on`, `entry`, `profile`) and backing (`dir`, `build`, `watch`,
`run`, `env`, `docker`, `startup_timeout_s`). `orchestrator.Flip` proves
that swapping the backing at runtime is safe as long as the port is stable:
it kills the current placement, starts the other via `startServiceAs`, and
the proxy listener bound at `Up` keeps forwarding.

## Goals / Non-Goals

**Goals:**
- Declare N backings for one service with one default; switch at runtime
  from REST/CLI/dashboard; override at `up`.
- Zero change for services without `variants:`.

**Non-Goals:**
- Folding `docker:` into the variant model (a variant may still declare
  both `run` and `docker` and be flipped between them). Possible later.
- Per-profile defaults (`profiles: {full: [monolith=real]}`). Round two.

## Decisions

### D1. Variants are flattened into a `config.Service` before the orchestrator sees them

`Config.ResolveService(name, variant) (Service, error)` returns a copy of
the service with the variant's backing fields overlaid (and `Variants`/
`Default` cleared). Every orchestrator start path (`startServiceAs`,
`Restart`, `Flip`) takes the resolved service, so none of the
build/run/health/docker code learns about variants. The orchestrator stores
only `variant map[string]string` (current choice per service) alongside
`procs`/`dockerNodes`, initialised from `Default` (or `Opts.Variants`).

*Alternative:* thread `variant` through `startServiceAs`. Rejected — it
would touch every call site for no benefit over resolving once.

### D2. Validation

- `variants:` and any service-level backing field together → error
  ("set backing fields on the variants, not the service").
- `default:` must name a declared variant; with exactly one variant it may
  be omitted (that variant is the default); with several it is required.
- Each variant must set `run` or `docker`; if any sets `run`, the service
  needs a `port` (same rule as today, applied per variant).
- `startup_timeout_s` ≥ 0 per variant.
- `default:` without `variants:` → error.

### D3. `SetVariant` is Flip with a different target

```go
func (o *Orchestrator) SetVariant(ctx, name, variant string) error
```
Under the service lock: validate the variant exists; if nothing is running
for `name`, record the choice and return (it takes effect on the next
start). Otherwise tear down whichever placement is live (same code Flip
uses, extracted into `stopCurrent(name)`), record the choice, and
`startServiceAs(resolved, defaultPlacement(resolved))`. Restart and Flip
resolve through the recorded choice so they never revert to `default`.

### D4. Per-variant build stamps

`<name>.buildstamp` → `<name>.<variant>.buildstamp` when a variant is in
play. Without it, building the stub stamps the service and the real
variant's first start skips its build. Logs stay `<name>.log` — it's one
logical service.

### D5. Surfaces

- `ServiceState.Variant` (`json:"variant,omitempty"`).
- `POST /api/services/{name}/variant` body `{"variant":"x"}` → 200 with the
  new `ServiceState`; 404 unknown service; 400 unknown variant or service
  without variants.
- `TopologyNode.Variant` / `TopologyNode.Variants []string` (omitempty) so
  the dashboard can render a selector only where it applies.
- CLI: `ensemble variant <service> <variant>`; `ensemble up --variant
  a=x,b=y`; `ensemble status` prints a VARIANT column.
- Dashboard: a `<select>` in the service panel next to Restart/Flip, shown
  only when `variants` is non-empty.

## Risks / Trade-offs

- [A variant switch on a running service is a hard kill + cold start, like
  Flip] → Same contract as Flip; the health gate decides when it's back.
- [`--variant` for a service without variants] → `up` fails fast with the
  same validation error the config layer produces.
- [Watch globs differ per variant] → they live on the variant, so staleness
  is computed against the right tree.

## Migration Plan

Additive. Existing configs unchanged.
