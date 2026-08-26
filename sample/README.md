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

**Not built yet:** the `rn-app` (Expo) client — the rest of task 5.2.

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

Both variants implement the identical `/orders` contract, so
`storefront-bff`/`ops-bff` never know (or care) which one they're talking
to — see `order-stub/main.go`'s doc comment.

## Run it

```sh
cd sample
ensemble up -c ensemble.yaml                       # money path, order defaults to the Go stub
ensemble up -c ensemble.yaml --variant order=real   # + the real order-svc (needs a JDK)
ensemble seed baseline                              # starter products + users
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

### Seeds

- `baseline` — a handful of starter products + users. The default.
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
one app — the point is proving retrace can eventually tap into either, not
testing the app twice (see `adapters/` in design.md §7, task 4.7 — the
`retrace-playwright`/`retrace-maestro` packages that would actually wire
these to retrace don't exist yet, so today these are two plain, independent
test suites).

Both need the full stack running and seeded first
(`ensemble up -c ensemble.yaml && ensemble seed baseline`) — they exercise
the real backend, not a mock.

```sh
cd clients/web-app
npm test                        # Playwright — tests/checkout.spec.js
maestro test maestro/checkout.yaml   # Maestro — web support is beta, Chromium-only
```

Maestro's `assertVisible` text is a **full regex match against an
element's entire text**, not a substring search — `"total: .*"`, not
`"total:"` (the flow's own comments call this out; it's easy to get wrong
once and burn the beta's ~15s-per-assertion timeout finding out).

## Layout

```
sample/
├── ensemble.yaml              # the reference config for the full stack
├── seeds/                     # baseline.sql, users.sql, empty.sql, bulk.sql
├── clients/
│   └── web-app/                # React/Vite — browse/cart/checkout, no tracing code
│       ├── tests/               # Playwright spec
│       └── maestro/             # Maestro web flow (beta)
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
