# ensemble-tui

## Purpose
TBD

## Requirements

### Requirement: `ensemble tui` connects to a running control plane
`ensemble tui` SHALL check that the control-plane API at the target URL
(default same as `ensemble dashboard`'s default, overridable with
`--api-url`) answers `GET /api/status` before entering the terminal UI. If
it is unreachable, the command SHALL print an error naming the URL and
suggesting `ensemble up` is not running, and exit non-zero, without opening
any terminal UI screen.

#### Scenario: Stack not running
- **WHEN** `ensemble tui` runs and nothing answers at the target API URL
- **THEN** the command prints an error including the URL and exits non-zero

#### Scenario: Stack running
- **WHEN** `ensemble tui` runs and the control plane answers `GET
  /api/status`
- **THEN** the terminal UI takes over the screen, starting on the Services
  panel

### Requirement: `ensemble up --tui` enters the terminal UI after startup
`ensemble up` SHALL accept a `--tui` flag. When set, once the stack has
finished starting (the same point at which plain `ensemble up` begins its
blocking status loop), `ensemble up` SHALL hand off to the same terminal UI
`ensemble tui` runs, against its own just-started control plane. Without
`--tui`, `ensemble up`'s behavior SHALL be unchanged from before this
capability existed.

#### Scenario: `--tui` passed
- **WHEN** `ensemble up --tui` is run and the stack starts successfully
- **THEN** the terminal UI is shown in place of the plain status-line loop

#### Scenario: `--tui` omitted
- **WHEN** `ensemble up` is run without `--tui`
- **THEN** it behaves exactly as before this capability existed (blocking
  status line until interrupted)

#### Scenario: Ctrl-C inside the TUI shuts down the stack
- **WHEN** `ensemble up --tui` is running and the user exits the terminal UI
  (e.g. presses `q` or Ctrl-C)
- **THEN** the stack started by this `ensemble up` invocation is shut down,
  the same as Ctrl-C during the plain blocking status loop

### Requirement: Services panel shows live health and supports actions
The terminal UI SHALL have a Services panel listing every service from `GET
/api/status`, refreshed on a timer, each row showing at least name, status
(healthy/unhealthy/etc.), and active variant. The panel SHALL support
per-service actions bound to keys: restart (`POST
/api/services/{name}/restart`), flip variant (`POST
/api/services/{name}/flip`), and re-seed (`POST /api/seed/{name}`), applied
to the currently selected row.

#### Scenario: Unhealthy service is visually distinct
- **WHEN** a service's status is not `healthy`
- **THEN** its row is visually distinguished (e.g. color) from healthy rows

#### Scenario: Restart action
- **WHEN** the user selects a service row and invokes the restart action
- **THEN** the TUI calls `POST /api/services/{name}/restart` for that
  service and reflects its updated status on the next refresh

### Requirement: Traffic panel streams live hops with a detail view
The terminal UI SHALL have a Traffic panel that subscribes to `GET
/api/traffic/stream` and appends each received hop to a scrolling list
showing at least From, To, method, path, status, and duration. The panel
SHALL support selecting a hop to view its full detail (headers/body where
present) and a toggle to filter the list to error hops only. On stream
disconnect, the TUI SHALL reconnect automatically rather than leaving the
panel silently frozen.

#### Scenario: New hop appears live
- **WHEN** a request passes through the running stack while the Traffic
  panel is open
- **THEN** a corresponding row appears in the hop list without user action

#### Scenario: Errors-only filter
- **WHEN** the user enables the errors-only filter
- **THEN** only hops with a non-empty error/non-2xx status remain visible

#### Scenario: Stream drops and recovers
- **WHEN** the SSE connection to `/api/traffic/stream` is lost
- **THEN** the TUI attempts to reconnect and resumes appending new hops once
  reconnected, without requiring the user to restart the TUI

### Requirement: Latency panel shows and controls injection rules
The terminal UI SHALL have a Latency panel listing rules from `GET
/api/latency`, refreshed on a timer, each row showing at least target, path,
and armed state. The panel SHALL support arming/disarming all rules (`POST
/api/latency/arm-all`) and resetting rules to defaults (`POST
/api/latency/reset`) via key bindings.

#### Scenario: Arm-all reflected
- **WHEN** the user invokes the arm-all action
- **THEN** the TUI calls `POST /api/latency/arm-all` and every rule's row
  shows armed on the next refresh

### Requirement: Profiles panel shows and controls profile state
The terminal UI SHALL have a Profiles panel listing profiles from `GET
/api/profiles`, refreshed on a timer, each row showing at least the profile
name and whether it is currently up. The panel SHALL support bringing the
selected profile up (`POST /api/profiles/{name}/up`) or down (`POST
/api/profiles/{name}/down`) via key bindings.

#### Scenario: Bring a profile up
- **WHEN** the user selects a profile that is down and invokes the "up"
  action
- **THEN** the TUI calls `POST /api/profiles/{name}/up` and the profile
  shows as up on the next refresh

### Requirement: Panel navigation
The terminal UI SHALL present the Services, Traffic, Latency, and Profiles
panels as switchable tabs, with exactly one panel visible at a time, and key
bindings to switch between them.

#### Scenario: Switch panels
- **WHEN** the user invokes the switch-panel key binding
- **THEN** the next panel in the tab order becomes visible and the
  previously visible panel's content is hidden
