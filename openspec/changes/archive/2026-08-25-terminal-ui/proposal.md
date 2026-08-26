# Proposal: terminal-ui

Status: proposed (2026-08-21).

## Why

`ensemble up` runs a whole local stack, but the only way to watch it is
`ensemble dashboard`, which shells out to a browser tab. That's a context
switch every developer running `ensemble up` in a terminal already pays for,
and it doesn't work at all over SSH or in a headless/CI-adjacent shell where
there's no browser to open. The control plane already exposes everything the
web dashboard needs over plain REST + SSE (`/api/status`, `/api/traffic`,
`/api/latency`, `/api/services/*`, `/api/profiles`, `/api/databases`,
`/api/entities`) — a terminal UI reading the same API gives developers who
live in the terminal (and anyone on a remote box) a first-class way to watch
services, traffic, and latency injection without ever leaving it.

## What Changes

- New `ensemble tui` subcommand: a terminal UI client of the running control
  plane's REST/SSE API (same contract the web dashboard uses, no new backend
  endpoints). Requires `ensemble up` already running and reachable, same
  reachability check as `cmdDashboard`.
- New `--tui` flag on `ensemble up` to launch straight into the terminal UI
  in the foreground after the stack comes up, in place of the current
  "block until Ctrl-C, print status line" behavior. Plain `ensemble up`
  (no flag) keeps today's behavior unchanged.
- TUI views map the web dashboard's tab set onto terminal panels, scoped to
  what's ergonomic in a fixed-width grid:
  - **Services**: live health/status per service (from `/api/status`,
    polled), with restart / flip-variant / seed actions bound to keys —
    the terminal analog of the header health strip plus quick actions the
    web UI currently buries in per-service controls.
  - **Traffic**: scrolling live hop log via the `/api/traffic/stream` SSE
    feed, filterable to errors-only, with a detail pane for the selected
    hop (method, path, status, timing) — maps to the web dashboard's
    Traffic + Inspector views collapsed into one list+detail layout, since a
    terminal has no room for the web UI's separate topology graph.
    Topology's *node/edge graph* itself is out of scope for v1 (a graph
    doesn't render well in a fixed-grid terminal) — Traffic's flat hop list
    already exposes From/To per hop, which is the information a developer
    actually needs during a debugging session.
  - **Latency**: list of configured latency-injection rules, with keys to
    arm/disarm/reset — terminal analog of the web Latency view's rule table.
  - **Profiles**: list of declared profiles and which are up, with keys to
    bring one up/down — matches the profile controls already in the API.
  - Entities/database inspection is explicitly **out of scope for v1** —
    those views are inherently tabular/interactive in a way a terminal grid
    serves poorly; can be revisited once the above land and prove the
    pattern.
- Read-only status output (no TTY, e.g. piped or `--no-tui` companion) is
  **not** part of this change — `ensemble tui` requires an interactive
  terminal; existing `ensemble dashboard --no-open` remains the way to get a
  scriptable URL.
- No changes to the control-plane API, `ensemble.yaml` schema, or the web
  dashboard — this is purely a new client.

## Capabilities

### New Capabilities
- `ensemble-tui`: a terminal UI (`ensemble tui`, and `ensemble up --tui`)
  that renders live service health, traffic, latency rules, and profile
  state from the existing control-plane REST/SSE API inside the terminal.

### Modified Capabilities
<!-- No main specs exist yet under openspec/specs/; ensemble up's --tui flag
     is captured as an ADDED requirement in the new capability rather than
     as a delta against a spec that has not been synced. -->

## Impact

- New `ensemble/cmd/ensemble/cmd_tui.go` (subcommand) and a new internal TUI
  package (e.g. `ensemble/tui/`) built on a terminal UI library (Bubble Tea
  is the natural fit given the existing Go toolchain — confirmed in design).
- `ensemble/cmd/ensemble/cmd_up.go`: new `--tui` flag; when set, `cmdUp`
  hands off to the TUI instead of its current blocking status loop once the
  stack is healthy.
- `ensemble/cmd/ensemble/main.go`: register the `tui` subcommand.
- No changes to `ensemble/server` routes — the TUI is a pure client of the
  existing API surface, same as `dashboard/ensemble-ui`.
- New Go module dependency for the TUI framework; no changes to the
  `dashboard/` frontend workspaces.
