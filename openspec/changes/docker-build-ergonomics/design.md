# Design: docker-build-ergonomics

## Decisions

- `runBuild(build, workDir, logPath)` opens the log in append mode (same
  0600 perms as the service log), writes a header line, and wires the
  command's stdout/stderr to a `MultiWriter` of the log and a bounded
  ring (`tailBuffer`, last 4 KB). On failure the error is
  `"<cmd>": <err>: <tail>` — unchanged shape, just bounded and no longer
  the only place the output lands. Orchestrator logs "building <name>
  (log: <path>)" and "built <name> in <dur>".
- `DockerPlacement.Args []string` is appended after ports/env and before
  the image in `dockerRunServiceArgs` (extracted so it's testable like the
  database args builders). No interpretation; validation only rejects an
  empty string entry.

## Non-Goals

Streaming build output over the API/SSE; a `build` timeout.
