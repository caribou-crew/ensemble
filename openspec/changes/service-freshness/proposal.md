## Why

A stack cloned via `repos.txt`/`setup.sh` drifts silently: someone pushes to the
branch you're tracking, or `main` advances and your feature branch falls
behind, and the only way to notice today is to `cd` into each service
directory and run `git fetch && git status` by hand. For a stack with 8+
service repos, this background staleness causes "works on my machine"
divergence and wastes time debugging before someone says "oh, just pull."
The Services tab already shows health, variant, placement, memory, and
uptime — everything about the *process* — but nothing about whether the
*code* it's running is current.

## What Changes

- Add optional `freshness:` config (`default_branch`, `poll_interval_s`) to
  `ensemble.yaml`.
- Add a background freshness checker in the orchestrator that periodically
  runs `git fetch` per service and computes how many commits the service's
  checkout is behind its own remote branch and behind the configured
  default branch.
- Extend `ServiceState` (and the `GET /api/status` response) with a
  `freshness` field carrying branch name, behind-counts, last-checked
  timestamp, and any fetch error.
- Add a `POST /api/freshness/check` endpoint to trigger an immediate
  re-check on demand.
- Add freshness badges to the dashboard Services tab (behind-origin,
  behind-default, unknown/error states) with a detail tooltip.
- Add freshness output to `ensemble status --json` (via the existing
  `ServiceState` payload) and a dedicated `ensemble freshness` table command.
- Skip services whose resolved `Dir` is not a git repository, or whose repo
  root is the same as the `ensemble.yaml` config's own repo root (stubs
  living alongside the config).

## Capabilities

### New Capabilities
- `service-freshness`: background git-fetch-based staleness detection per
  service (behind own remote branch, behind default branch), surfaced
  through orchestrator state, the status API, the CLI, and the dashboard.

### Modified Capabilities
(none — no existing capability's requirements change; this adds a new
field to the status payload without altering existing behavior)

## Impact

- `ensemble/config`: `Config` gains an optional `Freshness` block
  (`DefaultBranch`, `PollIntervalS`).
- `ensemble/orchestrator`: new `freshness.go` with a background goroutine
  lifecycle tied to `Up`/`Down`; `ServiceState` gains a `Freshness` field.
- `ensemble/server`: `GET /api/status` response grows the `freshness` field
  per service; new `POST /api/freshness/check` route.
- `dashboard/ensemble-ui`: `ServicesView.tsx` renders freshness badges and a
  "check now" affordance; `api/types.ts` gains a `freshness?` field.
- `ensemble` CLI: `status --json` includes freshness for free; optional new
  `ensemble freshness` command.
- No impact to `core/proxy`, tracing, recording, retrace, inspector, seeds,
  or entities.
- Non-breaking: stacks without `freshness:` config, or services whose `Dir`
  isn't a repo, simply omit the field.
