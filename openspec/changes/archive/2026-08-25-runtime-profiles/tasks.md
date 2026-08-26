# Tasks: runtime-profiles

## 1. Config + orchestrator

- [x] 1.1 `Config.ProfileNames()`, `ProfileMembers(name)` with tests.
- [x] 1.2 Active set as state; `activeServices()`; `topoOrder(set)`;
      idempotent `wireProxy`; `Profiles()`.
- [x] 1.3 `UpProfiles` / `DownProfiles` with tests: second lane joins
      (shared PID stable), refcounted down, re-up, unknown profile, always-on
      untouched.

## 2. Server + CLI

- [x] 2.1 `GET/POST /api/profiles…` with tests.
- [x] 2.2 `up`/`down` positional profiles (attach vs cold start), `profiles`
      command, client methods, usage; tests for the attach fork.

## 3. Dashboard + docs

- [x] 3.1 Profile toggle strip on TopologyView; tsc/vitest green.
- [x] 3.2 README "Profiles as lanes"; full sweep.
