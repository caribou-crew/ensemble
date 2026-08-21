## Context

`ensemble up` boots a stack (services, proxies, gateways, stubs) and today
just blocks in the foreground printing a status line until Ctrl-C, while
`ensemble dashboard` opens a browser tab pointed at the control plane's
built-in web UI (`ensemble/server/ui`, served at `/` from the API process
itself, built from `dashboard/ensemble-ui`). Everything that UI shows comes
from a small REST + SSE surface: `GET /api/status`, `GET /api/topology`,
`GET /api/traffic` + `GET /api/traffic/stream` (SSE), `GET/PUT/DELETE
/api/latency` + `/api/latency/arm-all` + `/api/latency/reset`, `POST
/api/services/{name}/restart|flip|variant`, `GET /api/profiles` + `POST
/api/profiles/{name}/up|down`, `POST /api/seed/{name}`, plus
databases/entities endpoints for inspection. None of that is
dashboard-specific plumbing — it's the same control-plane API any client can
call, which is what makes a terminal client viable without touching
`ensemble/server` at all.

`ensemble`'s own Go modules (`ensemble/go.mod`) currently pull in almost
nothing beyond DB drivers and yaml — no TUI framework exists in the repo
yet, so this is a new dependency, not a reuse of something already vendored.

## Goals / Non-Goals

**Goals:**
- Give developers running `ensemble up` in a terminal (including over SSH,
  where opening a browser isn't an option) a live view of service health,
  traffic, latency rules, and profiles without leaving the terminal.
- Reuse the existing control-plane API verbatim — the TUI is a client, like
  `dashboard/ensemble-ui`, not a second implementation of orchestration
  logic.
- Keep `ensemble tui` and `ensemble up --tui` thin wrappers around one
  shared TUI program, so both entry points stay in lockstep for free.

**Non-Goals:**
- Rendering the topology graph. A node/edge graph doesn't have a good
  terminal analog at typical terminal widths; Traffic's flat hop list
  (From/To per row) covers the "what's calling what" need well enough for
  v1. Revisit only if usage shows the flat list isn't enough.
- Entity/database inspection views. These are inherently tabular,
  drill-down UIs (schema browsing, row paging, arbitrary JSON bodies) that
  the web dashboard is much better suited to; out of scope until the
  simpler views above prove the pattern is worth extending.
- Any change to `ensemble/server`'s API surface, `ensemble.yaml` schema, or
  the web dashboard itself.
- A non-interactive/scriptable status mode (e.g. `--no-tui` printing JSON on
  a timer). `ensemble dashboard --no-open` already covers "get the URL
  without a browser"; a scripting-friendly status feed is a separate concern
  from an interactive TUI and not addressed here.

## Decisions

**TUI framework: Bubble Tea (`github.com/charmbracelet/bubbletea`) +
Bubbles/Lipgloss.** Chosen over building directly on `golang.org/x/term` or
`tcell` because Bubble Tea's Elm-style model/update/view loop maps cleanly
onto "poll REST on a tick + consume an SSE stream + handle key events," and
its companion `bubbles` library already has list/viewport/table components
that cover the Services/Traffic/Latency/Profiles panels without hand-rolling
scroll/selection logic. It's also the de facto standard for Go CLI TUIs at
this point, which matters for maintainability by future contributors.
Alternative considered: `tview` — more widget-complete out of the box, but
its retained-mode widget tree fits worse with a "re-render from fresh API
state every tick" model than Bubble Tea's pure `View()` render.

**One shared TUI program, two entry points.** `ensemble/tui` exposes a
single `Run(ctx, apiURL) error` (or similar) that both `cmd_tui.go` and
`cmd_up.go`'s `--tui` branch call. `ensemble tui` calls it against an
already-running stack (same reachability check as `cmdDashboard` — fail
fast with "is `ensemble up` running?" if the API doesn't answer).
`ensemble up --tui` calls it in-process after `cmdUp` finishes bringing the
stack up, replacing the current blocking status-line loop; Ctrl-C in the
TUI triggers the same shutdown path Ctrl-C triggers today.

**Polling for state, SSE for the traffic feed.** Services/Latency/Profiles
panels poll their REST endpoints on a timer (mirroring
`dashboard/ensemble-ui`'s `useHealthPoll`, ~2-5s), which is simple and
matches what the web dashboard already does for the same data. Traffic uses
the existing `/api/traffic/stream` SSE endpoint for a live-scrolling feed
rather than polling `/api/traffic`, since polling a log view produces
visible batching/jank that SSE avoids — same reasoning that led the web
dashboard to use SSE there. The TUI's SSE client reuses the same
reconnect-with-backoff shape as `dashboard/ensemble-ui/src/api/sse.ts`
(reconnect after a fixed delay on `error`), just in Go.

**Panel navigation via tabs, one panel visible at a time.** Mirrors the web
dashboard's `Tabs` component (`Services`/`Traffic`/`Latency`/`Profiles`)
rather than a persistent multi-pane split, to keep every panel usable at a
narrow terminal width (80 cols) instead of assuming a wide split-pane
terminal.

## Risks / Trade-offs

- [New Go dependency (Bubble Tea + Bubbles + Lipgloss) in a codebase that's
  been deliberately dependency-light] → Small, well-maintained, pure-Go
  libraries with no cgo; pinned like any other module dependency. Confined
  to the new `ensemble/tui` package — nothing else in the module tree
  depends on it.
- [`ensemble up --tui` changes the foreground behavior of an existing,
  scripted-around command] → Opt-in via a new flag; plain `ensemble up`
  keeps today's exact behavior, so nothing that shells out to `ensemble up`
  today breaks.
- [SSE reconnect-with-backoff logic is being written twice now — once in
  TypeScript for the web dashboard, once in Go for the TUI] → Both are thin
  (a few dozen lines) and the underlying contract (named SSE events over
  `/api/traffic/stream`) is stable; not worth a shared cross-language
  library for two small clients.
- [Terminal width/height variability — a narrow terminal (e.g. a small SSH
  session) could make the Traffic panel's columns unreadable] → Panels
  degrade by truncating least-important columns first (e.g. drop the detail
  pane before dropping method/path/status); exact column priority is a
  tasks-time implementation detail, not a blocking design question.

## Migration Plan

No migration — purely additive (new subcommand, new flag). No existing
behavior changes unless a user explicitly passes `--tui`.

## Open Questions

- Exact default poll interval for Services/Latency/Profiles panels (proposed
  2-5s, matching the web dashboard's `useHealthPoll` default) — settle at
  implementation time by feel, not a hard requirement.
- Whether `ensemble tui` should accept the same `--api-url` override
  `ensemble dashboard` does (likely yes, for consistency) — trivial to add,
  not a design-level decision.
