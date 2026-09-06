# sample stack ("brew")

The full "brew" sample stack is spec'd in
[`openspec/changes/init-ensemble-retrace/design.md`](../openspec/changes/init-ensemble-retrace/design.md#8-sample-stack-brew--coffee-ordering-storefront)
(§8) and [`tasks.md`](../openspec/changes/init-ensemble-retrace/tasks.md)
(Phase 5).

**Built:** all 7 backend services (`edge-gw`, `catalog-svc`, `user-svc`,
`order` [`order-stub` + `order-svc`], `notify-worker`, `storefront-bff`,
`ops-bff`), all 4 storage backends (Postgres, MySQL, Redis, DynamoDB
Local), the `payments` stub on the money path plus decorative
`analytics`/`kms` stubs, all 4 named seeds (`baseline`, `empty`, `bulk`,
`outage`), `entities:` over catalog's products, and the `web-app`
(React/Vite) browser client.

The `rn-app` (Expo) client was **dropped by decision** (2026-08-21, see
task 5.2 in tasks.md): its purpose was demonstrating retrace tapping into
a second test runner, and web-app's Playwright + Maestro flows cover that
without the RN/Expo toolchain weight.

Money path: `edge-gw` → `storefront-bff` → `order` → (`catalog-svc` +
`user-svc` + `payments` stub) → Redis → `notify-worker`. `ops-bff` is a
read-only internal aggregator over `catalog-svc`/`order`, off the money
path.

`order` exercises ensemble's `variants:` feature — one logical service, two
backings sharing the same port/proxy/health/depends_on. `stub`
(`order-stub`, plain Go, in-memory, no JDK/MySQL needed) is the default, so
the whole money path — checkout, `/admin/orders` — works the moment you run
`ensemble up`, no Java required. `real` is the Java/Spring/MySQL
implementation `order-svc` always was, opt in on demand:

```sh
ensemble variant order real   # swap the running stack to the JVM backend
ensemble variant order stub   # swap back
```

Both variants implement the same `/orders` API shape, so
`storefront-bff`/`ops-bff` never know (or care) which one they're talking
to — see `order-stub/main.go`'s doc comment. Use the
[variant comparison](docs/comparisons.md#try-the-existing-go-and-java-order-implementations)
to check their behavior on a recorded flow.

## Run it

Install Go 1.25+, pnpm, Docker, and the ensemble/retrace CLIs. From the
repository root, run `pnpm install --frozen-lockfile` and
`pnpm --filter '@caribou-crew/retrace-playwright...' run build`. Docker must
be running. Java 17 is needed for the optional Java order variant and for
Maestro; set `JAVA_HOME` before starting ensemble if your Java launcher
does not find it automatically.

Start the stack in one terminal and leave it running:

```sh
cd sample
ensemble up -c ensemble.yaml                       # money path, order defaults to the Go stub
```

To start with Java instead, use `ensemble up -c ensemble.yaml --variant
order=real`. In a second terminal, also from `sample/`:

```sh
ensemble ready --timeout 60s
ensemble seed baseline                            # starter products + users
```

`web` starts automatically with everything else — open
`http://127.0.0.1:9087` for the browser client, or drive edge-gw directly:

```sh
# browse the catalog (through edge-gw -> catalog-svc)
curl -H "Authorization: Bearer demo-token" http://127.0.0.1:9080/products

# add to cart (through edge-gw -> storefront-bff, DynamoDB-backed).
# 1 and 2 are the user ids baseline seeds — any other id 404s at checkout.
curl -X POST -H "Authorization: Bearer demo-token" -H "content-type: application/json" \
  -d '{"product_id":1,"quantity":2}' \
  http://127.0.0.1:9080/cart/1/items

# checkout — cart -> order -> catalog/user/payments -> redis -> notify.
# Works out of the box: order defaults to the order-stub variant.
curl -X POST -H "Authorization: Bearer demo-token" \
  http://127.0.0.1:9080/cart/1/checkout
```

Then `open http://127.0.0.1:4700` and look at the trace for the checkout
call, or `ensemble traffic --json` — one `traceId` covers the whole chain
down to the async `notify-worker` leg.

Hops from the browser app carry a `client` of `web`, shown as a small badge
in the traffic view. That takes no configuration: `clients/web-app` sends
`x-source-client`, one of the two headers ensemble checks by default. Add a
second front-end sending `admin` and the two are told apart everywhere the
hop is rendered. The value is validated as an identifier, so a malformed one
records as `client` and the original never reaches disk — set
`client_identity_headers:` in `ensemble.yaml` if your stack already spells
that header its own way.

`client` is not the same field as `from`. `from` is a position in the service
graph — which service called this hop — and on an entry hop there is no
service to name. `client` is which front-end started the request, and it is
the one safe to group by.

### Seeds

- `baseline` — inserts missing starter products + users. The default;
  it does not overwrite existing rows, clear carts, or erase order history.
- `empty` — truncates products and users (Postgres only; doesn't touch
  MySQL `orders`/`order_items`, which only exist once the `real` order
  variant has connected at least once).
- `bulk` — ~20 products, ~15 users, for pagination/perf demos.
- `outage` — arms a 5s fixed latency on `catalog` via `/api/latency`, to
  demo the dashboard's latency view.

```sh
ensemble seed outage
time curl -H "Authorization: Bearer demo-token" http://127.0.0.1:9080/products
ensemble latency reset   # clear it
```

### Readiness

`tools/readiness.yaml` (wired via ensemble.yaml's `readiness:` key) checks
catalog-svc's `/healthz` and ops-bff's `/healthz` (with a demo
`headers_from` auth script) once `ensemble up` has brought the stack
healthy. `ensemble ready` blocks on it:

```sh
ensemble up -c ensemble.yaml
ensemble ready       # blocks until both checks pass (or 30s), exits 0/1
ensemble status      # also shows "READINESS: 2/2 passed"
```

### Entities

`entities:` (wired via ensemble.yaml's `entities:` key) gives the
dashboard's Entities tab a generic CRUD view over catalog-svc's existing
`/products` REST resource — no per-entity code, just `base` + `id`:

```sh
open http://127.0.0.1:4700   # dashboard -> Entities tab -> products
# or drive it directly, same as the dashboard does:
curl -X POST -H "content-type: application/json" \
  -d '{"name":"Cortado","price_cents":425}' \
  http://127.0.0.1:9081/products
```

Because `base` points at catalog's *proxy* port (`9081`), that create shows
up in `ensemble traffic` too, same as any other captured hop.

## How the trace stays connected

- ensemble's proxy stamps `traceparent`/`baggage` automatically on every hop
  it fronts — no code in any service does anything for a plain
  reverse-proxied leg (e.g. `edge-gw` → `catalog-svc`).
- Wherever a service makes its own outbound call (`catalog-svc` → payments,
  `storefront-bff`/`ops-bff` → `catalog-svc`/`order`, `order` → its
  downstreams), it explicitly copies `traceparent`/`baggage` from the
  inbound request — the same ~5-line `forwardTraceHeaders`/
  `TraceHeaders.forward` pattern in every service (Go, Node, and Java) —
  including both `order` variants, `order-stub` and `order-svc`. That's the
  entire "propagation contract" from design.md §8: two headers, no ensemble
  dependency.
- Downstream calls always target the *proxy* port (e.g. `9081` for catalog,
  not its real port `8081`) — that's what puts the hop in the trace at all.
- A service behind a proxy whose real process is down still gets a `200`-
  reachable proxy that answers `502` with a **plain-text** body (`dial tcp
  ...: connection refused`), not a network-level failure — every service
  that calls another checks the response `content-type` before parsing
  JSON, rather than assuming a failed `fetch()`/`HttpClient` call is the
  only way a downstream can be unreachable.

## web-app

A minimal React/Vite SPA (`clients/web-app`) that calls edge-gw's proxy
port directly from the browser — browse, add to cart, remove, checkout, see
the confirmed order. No state management library, no router: one `App.jsx`
with `fetch`-backed handlers, matching the curl flow above one for one (see
`src/api.js`). No tracing code either — edge-gw's proxy stamps a fresh
`traceparent` on any request that doesn't already carry one, so a plain
browser `fetch()` is a valid trace root.

edge-gw is the only sample service that sets CORS headers
(`withCORS` in `main.go`) — real edge/envoy layers own CORS the same way
they own auth, so it belongs there rather than in the client.

### Test runners (Playwright + Maestro)

The same checkout flow, driven by two different test runners against the
one app — the point is proving retrace can tap into either, not testing the
app twice (see `adapters/` in design.md §7).

Both need the full stack running. Their shared setup applies the baseline
seed, checks that every seed step succeeded, empties carts for users `1`
and `999`, and reads both carts back to verify the cleanup. This recovers
from a previous checkout that stopped midway, including carts with several
products. Setup uses the normal edge proxy, outside the recording session.
It does not erase custom catalog data or order history; start from matching
fixtures when comparing two versions.

Install Playwright's browser once. Install the Maestro CLI separately if
you want to use it, with a working Java runtime. From `sample/`:

```sh
pnpm -C clients/web-app exec playwright install chromium
pnpm -C clients/web-app run e2e       # Playwright — tests/checkout.spec.js
pnpm -C clients/web-app run e2e:maestro # Maestro — maestro/checkout.yaml, Chromium
```

`web-app` belongs to the pnpm workspace and links the Playwright adapter
with `workspace:*`, sharing one `@playwright/test` instance. Its `test`
script runs only the host-side setup/runner unit tests, so
`pnpm -r --if-present test` does not need a live stack or launch browsers.
The browser suites are the explicit `e2e` and `e2e:maestro` commands.

The Maestro wrapper starts its own Vite server on port 5174, passes the
recording URL and marker handshake to the flow, and closes the server when
Maestro exits, and requires both screenshots even if Maestro exits zero.
In a recording, screenshots go into the run's `shots/` and
JUnit/debug output into `report/`. Standalone output goes under the ignored
`sample/.retrace/maestro-*` directory. It refuses a partial recording
handshake instead of silently recording the wrong server.

The raw `maestro test clients/web-app/maestro/checkout.yaml` flow still
targets the stack's existing port 5173 when no parameters are supplied;
use the wrapper above for fixture setup and retrace recording.

Maestro's `assertVisible` text is a **full regex match against an
element's entire text**, not a substring search — `"total: .*"`, not
`"total:"` (the flow's own comments call this out; it's easy to get wrong
once and burn the beta's ~15s-per-assertion timeout finding out).

## Recording it with retrace

`retrace.yaml` in this directory records the Playwright suite above as a
flow called `checkout`. Run it from `sample/` — retrace does not search
parent directories, on purpose.

```sh
ensemble up -c ensemble.yaml    # one terminal, leave it running
# In another terminal, from sample/:
retrace rekey --init             # first use only, if no team key is already configured
retrace run                      # Playwright checkout
```

The config demonstrates encrypted-field redaction and therefore requires a
team key even though this flow does not send that field. Use an existing
`RETRACE_RECORDING_KEY` (hex/base64) or initialize `.retrace/recording.key`
once. The file contains raw key bytes, is ignored by Git, and must stay
secret; do not replace a key used by existing recordings.

To record Maestro explicitly without changing the default Playwright flow:

```sh
retrace run --flow checkout-maestro -- pnpm -C clients/web-app run e2e:maestro
```

| Runner | Assertions | Recorded checkpoints | Flow groups |
| --- | --- | --- | --- |
| Playwright | Checkout, emptied cart, unknown-user rejection | `catalog`, `cart`, `cart-emptied`, `unknown-user-error` | `browse`, `checkout`, `unknown-user` |
| Maestro | Checkout and emptied cart | `catalog`, `cart` | `browse`, `checkout` |

Maestro asserts the confirmation text but does not screenshot its changing
order ID. Keep a separate baseline per runner: their checkpoint coverage
and browser geometry differ. Playwright uses a fixed 1280×720 viewport.

That prints something like:

```
retrace: recorded brew/checkout as 20260826T185853Z-<sha>
  wire 32 calls · 4 checkpoints · 3 flow parts · capture ok
```

The first run has nothing to compare against. Promote it, change
something, and record again:

```sh
retrace ref accept --flow checkout <run-id>
retrace run
retrace diff --flow checkout
```

`retrace serve` opens the same thing as a browsable report.

**How the app ends up talking to the recording edge.** `retrace run` hands
its test command a `RETRACE_PROXY_URL`. `clients/web-app/playwright.config.js`
reads it and starts a *second* Vite dev server — on 5174, not the 5173 one
`ensemble up` already started — with `VITE_EDGE_URL` pointed at it. A
second server is required rather than tidy: Vite substitutes
`VITE_EDGE_URL` into the bundle at transform time, so the running 5173
bundle still calls edge-gw directly and reusing it would record nothing at
all. `retrace run` would then report `capture: empty` rather than a diff —
which is the check catching exactly this mistake.

`tests/checkout.spec.js` imports `test` from
`@caribou-crew/retrace-playwright` instead of `@playwright/test`. That is
the same test function plus one fixture, `retrace`, carrying
`group`/`endGroup` (flow parts) and `checkpoint` (screenshots). All three
are no-ops when nothing is recording, so `pnpm -C clients/web-app run e2e`
behaves exactly as it did before and there is no second copy of the suite
to keep in sync.

**A clean run can report `changed`.** The browser loads the
catalog and the cart concurrently, each behind its own CORS preflight, so
two runs of identical code interleave those calls differently. retrace
reports the interleaving as `[moved]`. Moves are excluded from the wire
budget — every gate still passes — but they do reach the verdict. When the
per-call list is all `[identical]` and `[moved]`, the wire content did not
change under the configured tolerances. Read `retrace diff --json` too:
both capture verdicts must be `ok`, required evidence must be recorded,
and gates, pixel results, hop violations, and suppressions still matter.
Slower Maestro runs can also contain an additional browser `OPTIONS`
preflight. It remains visible as an extra call; inspect it rather than
assuming every run must have exactly the same raw request count.

Optional environment settings for the sample test commands:

| Variable | Purpose / default |
| --- | --- |
| `ENSEMBLE_API` | Fixture setup control plane, `http://127.0.0.1:4700` |
| `BREW_SETUP_EDGE_URL` | Fixture cleanup edge, `http://127.0.0.1:9080`; keep this outside the recording proxy |
| `BREW_TEST_PORT` | Test Vite port: 5174 for recording and Maestro, 5173 for standalone Playwright |
| `VITE_EDGE_URL` | Standalone app's edge override; recording uses `RETRACE_PROXY_URL` |
| `JAVA_HOME` | Java installation for Maestro and the optional Java order backend |

These settings configure the sample runners. If the control plane is also
on a custom port, point the ensemble/retrace CLIs at it using their
`--api-url` / `--ensemble` options.

## More walkthroughs

- [Trace a request and investigate latency with doctor, REST, and MCP](docs/observability.md).
- [Compare commits, independent repositories, or Go/Java order variants](docs/comparisons.md),
  including full commit assertions and a JSON check that requires pixel,
  wire, and hop evidence.

## Layout

```
sample/
├── ensemble.yaml              # the reference config for the full stack
├── retrace.yaml               # records the Playwright suite as the `checkout` flow
├── seeds/                     # baseline.sql, users.sql, empty.sql, bulk.sql
├── docs/                      # observability and migration comparison walkthroughs
├── clients/
│   └── web-app/                # React/Vite — browse/cart/checkout, no tracing code
│       ├── tests/               # Playwright spec
│       ├── maestro/             # Maestro web flow (beta)
│       └── tools/               # shared fixture setup, Maestro wrapper, unit tests
└── services/
    ├── edge-gw/                # Go   — entry + auth stub + CORS, routes to storefront/catalog
    ├── catalog-svc/            # Go   — Postgres CRUD, calls payments stub
    ├── user-svc/               # Node — Postgres CRUD (users.accounts schema)
    ├── order-stub/               # Go   — order's default variant: in-memory, no JDK
    ├── order-svc/                # Java/Spring/Gradle — order's "real" variant, MySQL
    ├── notify-worker/           # Go   — Redis BRPOP consumer, async tail
    ├── storefront-bff/          # Node — DynamoDB-backed cart, checkout
    └── ops-bff/                  # Node — read-only admin aggregator
```

Each backend service is its own module (Go module, `package.json`, or
Gradle project), deliberately outside the repo's `go.work` — that mirrors
how a real company's services would actually be separate repos, and it
means no service depends on anything in this monorepo (`core`, `ensemble`)
to run. Go services' `build:` step in `ensemble.yaml` sets `GOWORK=off`
because Go otherwise auto-detects the parent `go.work` and refuses to
build a module that isn't listed in it.
