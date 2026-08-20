# Contributing

Specs are the source of truth: see `openspec/`. New work starts as an openspec
change proposal (`openspec new change <id>`), gets designed and approved, then
implemented against its `tasks.md`.

Ground rules (from the init design):

- Hot paths are Go, stdlib-first. Justify every third-party Go dependency.
- One hop schema (`core/trace`) — no product-local copies.
- API-first parity: anything the dashboard or TUI can do must be a REST/SSE
  JSON call first.
- Redaction happens at capture, never post-hoc.
- `go test -race ./core/... ./ensemble/... ./retrace/...` and `pnpm -r test`
  must be green at every commit (run from repo root; bare `./...` does not
  resolve from the workspace root).
