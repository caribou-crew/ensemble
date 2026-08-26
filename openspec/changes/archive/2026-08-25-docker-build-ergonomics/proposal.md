# Proposal: docker-build-ergonomics

Status: proposed (2026-08-21).

## Why

A "real" service variant that is a container (`build: docker build …` +
`docker: {image: …}`) already works, but two things make it unpleasant:
a multi-minute build is invisible until it finishes (`runBuild` buffers
its output), and `docker:` offers no way to pass the `docker run` flags a
containerized service talking to host-side databases needs
(`--add-host=host.docker.internal:host-gateway`, `--network`, `-v`,
`--platform`).

## What Changes

- Build output streams to the service's log (`.ensemble/run/<name>.log`)
  as it happens, under a `=== build: <cmd> ===` header, so `tail -f`
  shows progress; a failing build's error still carries the last few KB of
  output so `lastErr` says why.
- `docker.args: [...]` — extra `docker run` flags appended verbatim before
  the image, for services and their variants.
- README documents both, with the containerized-real-variant example.

## Capabilities

### New Capabilities
- `docker-build-ergonomics`: streamed build logs and `docker.args`.

## Impact

- `ensemble/orchestrator`: `runBuild` signature, `dockerRunService` args.
- `ensemble/config`: `DockerPlacement.Args`.
- README.
