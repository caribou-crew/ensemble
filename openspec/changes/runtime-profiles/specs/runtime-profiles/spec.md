# runtime-profiles

## ADDED Requirements

### Requirement: Activate a profile on a running stack
The orchestrator SHALL activate a configured profile at runtime, starting
in dependency order every service the enlarged active set covers that is
not already running, health-gated, and wiring its proxy listener if it has
none. Already-running services SHALL be untouched.

#### Scenario: Second lane joins
- **WHEN** lane1 is active with `shared` and `a1` running and lane2
  (members `shared`, `b1`) is activated
- **THEN** only `b1` is started and `shared` keeps its PID

#### Scenario: Unknown profile
- **WHEN** a profile no service or `profiles:` group names is activated
- **THEN** the call fails and nothing changes

### Requirement: Deactivate a profile with reference counting
The orchestrator SHALL deactivate a profile, stopping — dependents first —
every running service no remaining active profile covers. A service with no
profile (always-on) or named by another active profile SHALL keep running.
Databases SHALL never be stopped by profile changes.

#### Scenario: Shared service survives
- **WHEN** lane1 and lane2 are active and lane2 is deactivated
- **THEN** `b1` is stopped and reported `stopped`, while `shared`, `a1`, and
  every database keep running

#### Scenario: Re-activate after down
- **WHEN** lane2 is deactivated and then activated again
- **THEN** `b1` starts again through its existing proxy listener

### Requirement: Surfaces
`GET /api/profiles` SHALL list every profile with its member services and
active flag; `POST /api/profiles/{name}/up` and `/down` SHALL switch one and
return the same listing (404 unknown). `ensemble up <profile…>` SHALL add
profiles to a running stack when one answers at the client URL, else start
the stack with them active; `ensemble down <profile…>` SHALL deactivate;
bare `ensemble down` SHALL remain full shutdown; `ensemble profiles` SHALL
list.

#### Scenario: Attach
- **WHEN** a stack is running and `ensemble up lane2` is run
- **THEN** lane2 is activated on it and the output says it attached

#### Scenario: Cold start
- **WHEN** no stack is running and `ensemble up lane1` is run
- **THEN** the stack starts with lane1 as its active profile set
