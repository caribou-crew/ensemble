## Purpose

Lets `retrace serve`, run standalone from anywhere inside a repo, show
every app that repo's `retrace.repo.yaml` declares in one dashboard and
REST API — regardless of which subdirectory each app's runs are recorded
under — while leaving every existing single-root behavior unchanged when
no repo config is present.

## ADDED Requirements

### Requirement: `retrace serve` aggregates the review queue across every mapped root
When `retrace.repo.yaml` is found (per `retrace-repo-config`'s discovery
requirement), `GET /api/queue` SHALL return one worst-first list combining
every app from every root the repo config maps — computed by the same
per-root queue-building logic `retrace serve` already uses for a single
root — rather than only the apps under the working directory's own
`.retrace/runs/` tree. With no repo config found, `GET /api/queue` SHALL
behave exactly as it does today (the one root under the working
directory).

#### Scenario: Apps from different roots appear together
- **WHEN** `retrace.repo.yaml` maps `uxt-web` to root `.` and six mobile
  app keys to root `apps/sample/react-native`, each root has recorded runs
  for its own apps, and `retrace serve` starts from the repo root
- **THEN** `GET /api/queue` returns items for all seven apps in one
  worst-first list

#### Scenario: Started from a nested app directory still aggregates everything
- **WHEN** the same repo config as above exists and `retrace serve` starts
  from `apps/sample/react-native` instead of the repo root
- **THEN** `GET /api/queue` still returns items for all seven apps, not
  only the six recorded under that directory

#### Scenario: No repo config means today's single-root behavior
- **WHEN** no `retrace.repo.yaml` is found above the working directory
- **THEN** `GET /api/queue` returns only the apps recorded under the
  working directory's own `.retrace/runs/` tree, exactly as before this
  capability existed

### Requirement: Per-flow routes resolve an app to its own root
Every per-flow route (`GET /api/queue/{app}/{flow}`, shot, evidence,
video, and report routes, and the config-mutating routes such as
`POST .../rule`) SHALL resolve `{app}` to the root `retrace.repo.yaml`
maps it to and operate against that root's own `retrace.yaml`/`.retrace/`
tree. An `{app}` value not present in the repo config's app map SHALL
return the same "not found" response the route already gives for an
unknown app today.

#### Scenario: Detail route for an app in a different root than the request came from
- **WHEN** `retrace serve` is aggregating the two-root repo config above,
  started from the repo root, and a client requests
  `GET /api/queue/uxt-rn-ios/checkout`
- **THEN** the response is computed against
  `apps/sample/react-native`'s `retrace.yaml` and `.retrace/` tree, not
  the repo root's

#### Scenario: Unknown app under an active repo config
- **WHEN** a repo config is active and a client requests
  `GET /api/queue/no-such-app/checkout`
- **THEN** the response is the same "not found" error an unmapped app name
  produces today
