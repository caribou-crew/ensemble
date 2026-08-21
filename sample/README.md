# sample stack ("brew")

The full "brew" sample stack is spec'd in
[`openspec/changes/init-ensemble-retrace/design.md`](../openspec/changes/init-ensemble-retrace/design.md#8-sample-stack-brew--coffee-ordering-storefront)
(§8) and [`tasks.md`](../openspec/changes/init-ensemble-retrace/tasks.md)
(Phase 5).

**Built:** all 7 backend services (`edge-gw`, `catalog-svc`, `user-svc`,
`order-svc`, `notify-worker`, `storefront-bff`, `ops-bff`), all 4 storage
backends (Postgres, MySQL, Redis, DynamoDB Local), the `payments` stub on
the money path plus decorative `analytics`/`kms` stubs, and all 4 named
seeds (`baseline`, `empty`, `bulk`, `outage`).

**Not built yet:** the `web-app` (React/Vite) and `rn-app` (Expo) clients —
task 5.2, deferred so the backend money path could be finished and
verified first. Everything below is driven with `curl`.

Money path: `edge-gw` → `storefront-bff` → `order-svc` → (`catalog-svc` +
`user-svc` + `payments` stub) → Redis → `notify-worker`. `ops-bff` is a
read-only internal aggregator over `catalog-svc`/`order-svc`, off the money
path. `order-svc` (the only JVM service) runs behind `profile: full` — the
rest of the stack runs and degrades gracefully without it: `storefront-bff`
returns `503 {"error":"ordering unavailable"}` from checkout, `ops-bff`
returns `503 {"error":"orders unavailable"}` from `/admin/orders`, and
neither has a hard `depends_on` on `order`.

## Run it

```sh
cd sample
ensemble up -c ensemble.yaml                # money path only
ensemble up -c ensemble.yaml --profile full  # + order-svc (needs a JDK)
ensemble seed baseline                       # starter products + users
```

```sh
# browse the catalog (through edge-gw -> catalog-svc)
curl -H "Authorization: Bearer demo-token" http://127.0.0.1:9080/products

# add to cart (through edge-gw -> storefront-bff, DynamoDB-backed)
curl -X POST -H "Authorization: Bearer demo-token" -H "content-type: application/json" \
  -d '{"product_id":1,"quantity":2}' \
  http://127.0.0.1:9080/cart/42/items

# checkout — cart -> order-svc -> catalog/user/payments -> redis -> notify.
# Needs --profile full, else 503 "ordering unavailable".
curl -X POST -H "Authorization: Bearer demo-token" \
  http://127.0.0.1:9080/cart/42/checkout
```

Then `open http://127.0.0.1:4700` and look at the trace for the checkout
call, or `ensemble traffic --json` — one `traceId` covers the whole chain
down to the async `notify-worker` leg.

### Seeds

- `baseline` — a handful of starter products + users. The default.
- `empty` — truncates products and users (Postgres only; doesn't touch
  MySQL `orders`/`order_items`, which may not exist if `full` was never
  activated).
- `bulk` — ~20 products, ~15 users, for pagination/perf demos.
- `outage` — arms a 5s fixed latency on `catalog` via `/api/latency`, to
  demo the dashboard's latency view.

```sh
ensemble seed outage
time curl -H "Authorization: Bearer demo-token" http://127.0.0.1:9080/products
ensemble latency reset   # clear it
```

## How the trace stays connected

- ensemble's proxy stamps `traceparent`/`baggage` automatically on every hop
  it fronts — no code in any service does anything for a plain
  reverse-proxied leg (e.g. `edge-gw` → `catalog-svc`).
- Wherever a service makes its own outbound call (`catalog-svc` → payments,
  `storefront-bff`/`ops-bff` → `catalog-svc`/`order-svc`, `order-svc` → its
  downstreams), it explicitly copies `traceparent`/`baggage` from the
  inbound request — the same ~5-line `forwardTraceHeaders`/
  `TraceHeaders.forward` pattern in every service (Go, Node, and Java).
  That's the entire "propagation contract" from design.md §8: two headers,
  no ensemble dependency.
- Downstream calls always target the *proxy* port (e.g. `9081` for catalog,
  not its real port `8081`) — that's what puts the hop in the trace at all.
- A service behind a proxy whose real process is down still gets a `200`-
  reachable proxy that answers `502` with a **plain-text** body (`dial tcp
  ...: connection refused`), not a network-level failure — every service
  that calls another checks the response `content-type` before parsing
  JSON, rather than assuming a failed `fetch()`/`HttpClient` call is the
  only way a downstream can be unreachable.

## Layout

```
sample/
├── ensemble.yaml              # the reference config for the full stack
├── seeds/                     # baseline.sql, users.sql, empty.sql, bulk.sql
└── services/
    ├── edge-gw/                # Go   — entry + auth stub, routes to storefront/catalog
    ├── catalog-svc/            # Go   — Postgres CRUD, calls payments stub
    ├── user-svc/               # Node — Postgres CRUD (users.accounts schema)
    ├── order-svc/               # Java/Spring/Gradle — MySQL, profile `full`
    ├── notify-worker/           # Go   — Redis BRPOP consumer, async tail
    ├── storefront-bff/          # Node — DynamoDB-backed cart, checkout
    └── ops-bff/                  # Node — read-only admin aggregator
```

Each service is its own module (Go module, `package.json`, or Gradle
project), deliberately outside the repo's `go.work` — that mirrors how a
real company's services would actually be separate repos, and it means no
service depends on anything in this monorepo (`core`, `ensemble`) to run.
Go services' `build:` step in `ensemble.yaml` sets `GOWORK=off` because Go
otherwise auto-detects the parent `go.work` and refuses to build a module
that isn't listed in it.
