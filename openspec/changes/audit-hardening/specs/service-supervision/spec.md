## ADDED Requirements

### Requirement: Process exit is detected and stated
When a native service process exits after a successful start, the
orchestrator SHALL record its exit code (or signal) and exit time, move
the service to status `exited` (zero exit) or `crashed` (non-zero exit or
signal), populate `lastErr` with the tail of the service's log, and emit a
status event on the SSE stream. A docker-placed service whose container is
gone SHALL reach the same states via the existing health polling. The
orchestrator SHALL NOT auto-restart.

#### Scenario: Service crashes after startup
- **WHEN** a healthy service's process exits with code 1
- **THEN** `ensemble status` shows `crashed` with the exit code and a log
  tail, the dashboard updates without a refresh, and no restart is
  attempted

#### Scenario: Clean exit
- **WHEN** the process exits with code 0
- **THEN** status shows `exited`, distinct from `crashed` and from
  `stopped` (operator-initiated)

#### Scenario: Readiness treats crashed as failed
- **WHEN** a service is `crashed` while `ensemble ready` is waiting
- **THEN** `ready` exits non-zero without waiting for the full timeout

### Requirement: Service logs are readable over the API
`GET /api/services/{name}/logs?tail=N` SHALL return the last N lines
(default 200, capped) of `.ensemble/run/<name>.log`, and
`GET /api/services/{name}/logs/stream` SHALL follow it over SSE. Unknown
service names SHALL 404; a service with no log file yet SHALL return
empty, not error.

#### Scenario: Tail a service log
- **WHEN** a client requests `?tail=50` for a running service
- **THEN** the last 50 log lines are returned, including build/hook
  sections already present in the file

### Requirement: Logs are visible in dashboard and TUI
The dashboard Services tab SHALL offer a log pane per service (tail +
follow), and the TUI Services panel SHALL open a log tail for the
selected service.

#### Scenario: Dashboard log pane
- **WHEN** the user opens a service's log pane during a build
- **THEN** build output streams live without leaving the browser
