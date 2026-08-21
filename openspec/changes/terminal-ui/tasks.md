## 1. Foundation

- [x] 1.1 Add Bubble Tea / Bubbles / Lipgloss dependencies to
      `ensemble/go.mod`.
- [x] 1.2 Create `ensemble/tui` package with a top-level
      `Run(ctx context.Context, apiURL string) error` entry point and a
      minimal Bubble Tea program (empty shell, tab bar, quit on `q`/Ctrl-C).
- [x] 1.3 Reachability check on entry: call `GET /api/status` before
      starting the program; on failure, return an error the caller can print
      (mirrors `cmdDashboard`'s check) instead of opening the terminal UI.
- [x] 1.4 Go client for the control-plane REST endpoints the TUI needs
      (status, traffic, latency, profiles, services actions, seed) — a Go
      analog of `dashboard/ensemble-ui/src/api/client.ts`, scoped to only
      the endpoints this capability uses.
- [x] 1.5 Go SSE client for `/api/traffic/stream` with reconnect-on-error
      backoff, analogous to `dashboard/ensemble-ui/src/api/sse.ts`.

## 2. Services panel

- [x] 2.1 Poll `GET /api/status` on a timer; render a table (name, status,
      variant) via `bubbles/table`, unhealthy rows visually distinguished.
- [x] 2.2 Key bindings on the selected row: restart, flip variant, re-seed;
      wire to the corresponding REST calls.
- [x] 2.3 Tests: poll updates the table; each action key calls the right
      endpoint for the selected service.

## 3. Traffic panel

- [x] 3.1 Subscribe to the SSE hop stream on panel mount; append incoming
      hops to a scrolling list (from, to, method, path, status, duration).
- [x] 3.2 Detail view for the selected hop (headers/body where present).
- [x] 3.3 Errors-only filter toggle.
- [x] 3.4 Reconnect behavior on stream drop; verify the panel keeps
      appending hops after a simulated disconnect.
- [x] 3.5 Tests: incoming SSE frames render as rows; filter hides non-error
      hops; reconnect resumes appending.

## 4. Latency panel

- [x] 4.1 Poll `GET /api/latency` on a timer; render rules (target, path,
      armed) via `bubbles/table`.
- [x] 4.2 Key bindings for arm-all and reset, wired to their endpoints.
- [x] 4.3 Tests: poll updates the table; arm-all/reset call the right
      endpoints and the table reflects the response.

## 5. Profiles panel

- [x] 5.1 Poll `GET /api/profiles` on a timer; render profiles (name, up
      state) via `bubbles/table`.
- [x] 5.2 Key bindings for up/down on the selected profile, wired to their
      endpoints.
- [x] 5.3 Tests: poll updates the table; up/down call the right endpoint for
      the selected profile.

## 6. Navigation and shell

- [x] 6.1 Tab bar across Services/Traffic/Latency/Profiles with a key
      binding to cycle panels, exactly one panel visible at a time.
- [x] 6.2 Global key bindings: quit, help/key-hint footer.
- [x] 6.3 Graceful handling of narrow terminal widths (column
      truncation/priority per design.md's risk note).

## 7. CLI wiring

- [x] 7.1 New `ensemble/cmd/ensemble/cmd_tui.go`: `ensemble tui` subcommand
      with `--api-url` flag (default matches `cmdDashboard`'s), calling
      `ensemble/tui.Run`.
- [x] 7.2 Register `tui` in `ensemble/cmd/ensemble/main.go`'s subcommand
      dispatch.
- [x] 7.3 New `--tui` flag on `ensemble up` (`cmd_up.go`): after the stack
      finishes starting, call `ensemble/tui.Run` against the just-started
      control plane instead of the current blocking status-line loop; Ctrl-C
      inside the TUI shuts the stack down the same way today's status loop
      does.
- [x] 7.4 Tests: `--tui` omitted leaves `ensemble up`'s existing behavior
      unchanged; `ensemble tui` against an unreachable API prints an error
      and exits non-zero without entering the terminal UI.

## 8. Docs

- [x] 8.1 README: document `ensemble tui` and `ensemble up --tui` alongside
      the existing `ensemble dashboard` entry.
