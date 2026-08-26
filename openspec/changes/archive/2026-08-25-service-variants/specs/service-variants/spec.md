# service-variants

## ADDED Requirements

### Requirement: Named backings with a default
A service MAY declare `variants:` — a map of named backings, each carrying
`dir`, `build`, `watch`, `run`, `env`, `docker`, and `startup_timeout_s` —
and `default:` naming the variant `ensemble up` starts. Service-level
`port`, `proxy`, `health`, `depends_on`, `entry`, and `profile` SHALL apply
to every variant.

#### Scenario: Stub by default, monolith on demand
- **WHEN** `monolith` declares variants `stub` and `real` with `default: stub`
- **THEN** `ensemble up` starts `stub` with the service's port, proxy, health
  path, and dependencies

#### Scenario: Single variant needs no default
- **WHEN** a service declares exactly one variant and no `default`
- **THEN** that variant is the default

### Requirement: Validation
`Validate` SHALL reject: `variants:` together with any service-level
backing field; a `default` naming no declared variant; more than one
variant without `default`; a variant with neither `run` nor `docker`; a
variant with `run` on a service without `port`; `default` without
`variants`.

#### Scenario: Backing field on both levels
- **WHEN** a service sets `run` and also declares `variants`
- **THEN** `Load` fails telling the user to move the backing fields onto the
  variants

#### Scenario: Unknown default
- **WHEN** `default: prod` but only `stub` and `real` are declared
- **THEN** `Load` fails naming `prod`

### Requirement: Runtime switch
The orchestrator SHALL switch a service to a named variant at runtime
without touching its proxy listener: a running service is stopped and the
variant started in its default placement, health-gated; a stopped service
only records the choice for its next start. `Restart` and `Flip` SHALL keep
the current variant.

#### Scenario: Swap stub for real
- **WHEN** `monolith` runs as `stub` and a switch to `real` is requested
- **THEN** the stub process is killed, `real` is built if stale and started,
  `ServiceState.variant` reads `real`, and `ProxyPort` is unchanged

#### Scenario: Restart keeps variant
- **WHEN** `monolith` was switched to `real` and is then restarted
- **THEN** it restarts as `real`

#### Scenario: Unknown variant
- **WHEN** a switch names a variant the service does not declare
- **THEN** the switch fails naming the variant and nothing is stopped

### Requirement: Per-variant build staleness
Build stamps SHALL be kept per variant so switching to a variant whose build
is stale rebuilds it.

#### Scenario: First switch builds
- **WHEN** `stub` has been built and `real` never has
- **THEN** switching to `real` runs `real`'s build

### Requirement: Surfaces
`POST /api/services/{name}/variant` with `{"variant": "<v>"}` SHALL perform
the switch and return the new `ServiceState`; `ensemble variant <service>
<variant>` and `ensemble up --variant svc=v[,…]` SHALL drive it; topology
nodes SHALL expose `variant` and `variants`; `ensemble status` SHALL show a
VARIANT column.

#### Scenario: REST switch
- **WHEN** `POST /api/services/monolith/variant` with `{"variant":"real"}`
- **THEN** the response is 200 with `variant: "real"`

#### Scenario: Startup override
- **WHEN** `ensemble up --variant monolith=real`
- **THEN** `monolith` starts as `real` even though `default: stub`

#### Scenario: Bad request
- **WHEN** the body names an unknown variant, or the service has no variants
- **THEN** the response is 400 and the service is untouched
