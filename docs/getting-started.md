# Getting started: your own stack

This is the bring-your-own-stack walkthrough: two real services, one stub,
one `ensemble.yaml`, and a first recorded flow. The [sample "brew"
stack](../sample/) is the full-featured version of everything here —
databases, gateways, variants, seeds, two test runners — and its README
walks that config feature by feature. This page is the minimal path.

Install first (see the [README's "Try it"](../README.md#try-it) — `make
deps && make install`, or the published npm packages
`@caribou-crew/ensemble` and `@caribou-crew/retrace`).

## A minimal ensemble.yaml

Say your app is a Node front-end service that calls a Go API, and the API
calls a payments provider you can't run locally. In the directory that
owns the stack:

```yaml
services:
  web:
    dir: ./services/web           # working directory for `run`
    run: node server.js           # however this service starts
    port: 3000                    # the port the service itself listens on
    proxy: 9300                   # ensemble's intercept port in front of it
    health: /healthz              # polled until it answers, before up returns
    entry: true                   # clients call this one directly
    depends_on: [api]
    env:
      API_URL: http://127.0.0.1:9400   # api's PROXY port — see below

  api:
    dir: ./services/api
    build: go build -o api .      # optional; re-run when watch globs change
    run: ./api
    port: 4000
    proxy: 9400
    health: /healthz
    env:
      PAYMENTS_URL: http://127.0.0.1:9500

stubs:                            # dependencies you can't run locally
  payments:
    port: 9500
    routes:
      - match: { method: POST, path: /charges }
        respond:
          status: 201
          headers: { content-type: application/json }
          body: '{"id":"ch_1","status":"succeeded"}'
```

Nothing in either service changes for ensemble's sake: `run:` is whatever
already starts it, `health:` is whatever health route it already has, and
the stub answers the payments call the API was already making — just at an
address you control.

Then:

```sh
ensemble up
```

`up` starts both services in dependency order, health-gates each, brings
the stub up, and serves the dashboard and REST API on `127.0.0.1:4700`.
`ensemble status` shows the same table in the terminal; `ensemble down`
stops everything.

## The one wiring rule

Each service gets **two** ports: its real `port:` and ensemble's `proxy:`
intercept port in front of it. Traffic through the proxy port is captured
— trace id, correlation id, timings — and traffic straight to the real
port is invisible. So everything that *calls* a service points at the
proxy port:

- `web`'s `API_URL` above says `9400` (api's proxy), not `4000`.
- Your browser, curl, and test suite call `http://127.0.0.1:9300` (web's
  proxy), not `3000`.

Get it wrong and the stack still works — that's exactly what makes the
mistake easy to miss — but the hops between those two services never
appear. `ensemble up` checks for this: an `env:` value that references
another service's *real* port when that service has a proxy port draws a
wiring warning naming both ports, in the `up` output, in `ensemble status`
(a `warnings` field, `--json` included), and as a badge on the dashboard's
Services tab. The stack starts anyway — calling a real port directly is
legal — but the warning is telling you those hops will bypass capture.

## A tour of the dashboard

`ensemble dashboard` opens `http://127.0.0.1:4700`:

- **Topology** — the graph this config declares: `web → api → payments`,
  with live health and traffic-hot highlighting. If a service you expected
  to see traffic between shows a cold edge, that's the wiring rule above.
- **Traffic** — every captured hop, live. Drive one request through the
  stack and watch it:

  ```sh
  curl http://127.0.0.1:9300/checkout -X POST
  ```

  One trace covers `client → web → api → payments` — the stub's answer is
  a hop like any other. "Load earlier" pages back through persisted
  traffic from before the dashboard was open, and any hop expands into
  headers, bodies, timings, and an export (HAR/curl/raw).
- **Latency** — arm a delay on `api` without touching its code, and watch
  `web`'s timeout handling actually run.
- **Services** — start/stop/restart, a live log pane per service (build
  output included), and the wiring-warning badge. A service whose process
  dies shows `crashed` (or `exited`, for a clean zero) with its exit code
  and log tail — never a stale `healthy`.

The Inspector and Entities tabs light up once the config declares
`databases:` and `entities:` — see the [sample](../sample/) for those.

## Your first retrace flow

Recording needs a flow: a test command retrace can run while it captures
everything the app does. With Playwright, the adapter is one import — in
your suite, replace `@playwright/test`:

```ts
import { test } from '@caribou-crew/retrace-playwright';

test('checkout', async ({ page, retrace }) => {
  await page.goto('/');
  await retrace.checkpoint('landing');       // screenshot, compared pixel-wise later
  await page.getByRole('button', { name: 'Buy now' }).click();
  await retrace.checkpoint('confirmation');
});
```

`checkpoint` (and `group`/`endGroup` for marking flow parts) are no-ops
when nothing is recording, so the suite still runs standalone. The one
real requirement: the app under test must read `RETRACE_PROXY_URL` when it
is set and send its traffic there — that's the recording edge. A
hard-coded base URL records nothing, and retrace says so rather than
reporting a hollow pass.

Next to `ensemble.yaml`, a `retrace.yaml`:

```yaml
app: myapp

# The ensemble service whose proxy is the recording edge. With ensemble
# up, retrace attaches to the control plane and records the whole chain —
# client -> web -> api -> payments.
entry: web

# Standalone fallback: where the recording proxy forwards when ensemble
# is not running. Same address the app uses when nothing is recording.
upstream: http://127.0.0.1:9300

flows:
  checkout:
    command: npx playwright test tests/checkout.spec.ts
```

Then record, promote, and diff:

```sh
ensemble up                       # one terminal, leave it running
retrace run --flow checkout       # another — records the flow
retrace ref accept --flow checkout   # promote the run you just inspected
# ...change something...
retrace run --flow checkout
retrace diff --flow checkout      # pixel + wire + hop verdict, exit-coded
```

`retrace serve` opens the diff as a browsable review queue. From here,
[reference-lifecycle.md](reference-lifecycle.md) covers the full loop —
committing `.retrace-ref/`, the accept-time secret scan, reviewing
intentional changes, tolerance rules — and
[retrace-ci-example.yml](retrace-ci-example.yml) shows the CI half,
including replaying the committed reference as strict mocks with no stack
running at all.
