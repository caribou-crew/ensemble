# AGENTS.md

Guidance for coding agents working in this repository. It is loaded on every
session, so it stays short; the procedures live in `.claude/skills/`.

## What this repo is

A Go workspace with two products that share `core/`:

- **ensemble** (`ensemble/`) — runs a backend topology locally from
  `ensemble.yaml`: services, databases, stubs, telemetry proxies, latency
  injection, seeds.
- **retrace** (`retrace/`) — records a flow through that stack, replays it as
  strict mocks in CI, and diffs two recordings on pixels, wire traffic and
  provider hops.

`sample/` is a complete working stack ("brew") used for dogfooding. `adapters/`
holds the JS/Playwright/Maestro test-runner adapters, `dashboard/` the two web
UIs, `openspec/` the specs that are the source of truth.

## Commands that are easy to get wrong

```sh
# Go — bare ./... does NOT resolve from the workspace root.
go test -race ./core/... ./ensemble/... ./retrace/...
go vet ./core/... ./ensemble/... ./retrace/...

# JS — this is a pnpm workspace, NOT npm workspaces.
# `npm test --workspaces` fails with "No workspaces found!".
pnpm -r --if-present test
```

`retrace/cmd/retrace`'s suite takes ~4 minutes; it builds a real binary and
execs it. Budget for that rather than assuming it hung. Piping `go test`
through `grep` buffers the output — redirect to a file instead.

## House rules

These come from `openspec/` and are not negotiable:

- Hot paths are Go, stdlib-first. Justify every third-party Go dependency.
- One hop schema (`core/trace`) — no product-local copies.
- API-first parity: anything the dashboard or TUI can do must be a REST/SSE
  JSON call first.
- Redaction happens at capture, never post-hoc.
- Both suites green at every commit.

**Zero values must fail closed.** This codebase repeatedly chooses the
protective reading: `runs.Counts{}` means "not recorded", not "recorded and
clean"; an unassessed capture verdict is rejected rather than treated as
passing. When you add a field, ask what its zero value asserts, and make sure
that is the cautious answer rather than the convenient one.

**A test that cannot fail for the right reason is not a test.** Verify new
assertions against a deliberate mutation of the code they cover. A mutation
that fails to compile is not a mutation test — it proves nothing about the
assertion.

## Verifying a change you made to a client

Do not ask a human whether the UI still looks right. Record the flow and diff
it. Use the **`retrace-iterate`** skill
(`.claude/skills/retrace-iterate/SKILL.md`) — it is the capture → diff → read
the verdict → fix → recapture loop, including the ways the tool can be made to
lie.

The one rule worth stating here too: **never branch on the exit code alone.**
Read `retrace diff --json`. The exit code collapses distinct situations that
call for opposite responses.

## Concurrency

More than one agent session may be working in this tree at once. Stage commits
by explicit pathspec — never `git add -A` or `git add .`, which will sweep up a
peer's half-finished work and put your name on it.
