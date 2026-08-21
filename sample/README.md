# sample stack ("brew") — vertical slice 1

The full "brew" sample stack is spec'd in
[`openspec/changes/init-ensemble-retrace/design.md`](../openspec/changes/init-ensemble-retrace/design.md#8-sample-stack-brew--coffee-ordering-storefront)
(§8) and [`tasks.md`](../openspec/changes/init-ensemble-retrace/tasks.md)
(Phase 5): 7 backend services across Go/Node/Java, 2 clients, 4 storage
backends. This is the first slice of that — enough to prove a real trace
flows end to end through ensemble, before building out the rest.

**Built:** `edge-gw` (auth stub) → `catalog-svc` (Postgres, real CRUD) →
`payments` stub. One trace id spans all three hops.

**Not built yet:** `user-svc`, `order-svc`, `notify-worker`,
`storefront-bff`, `ops-bff`, the `web-app`/`rn-app` clients, and the
`analytics`/`kms` stubs (tasks 5.1/5.2 remainder).

## Run it

```sh
cd sample
ensemble up -c ensemble.yaml
ensemble seed baseline      # inserts a few starter products
```

```sh
# unauthorized — edge-gw's auth stub rejects it
curl -i http://127.0.0.1:9080/products

# list products (through edge-gw -> catalog-svc)
curl -H "Authorization: Bearer demo-token" http://127.0.0.1:9080/products

# create one
curl -X POST -H "Authorization: Bearer demo-token" -H "content-type: application/json" \
  -d '{"name":"latte","price_cents":450}' \
  http://127.0.0.1:9080/products

# "buy" it — catalog-svc calls the payments stub itself, so this is the
# 3-hop request: client -> edge-gw -> catalog-svc -> payments
curl -X POST -H "Authorization: Bearer demo-token" \
  http://127.0.0.1:9080/products/1/purchase
```

Then `open http://127.0.0.1:4700` and look at the trace for the `/purchase`
call, or `ensemble traffic --json` — one `traceId` covers all three hops.

## How the trace stays connected

- ensemble's proxy stamps `traceparent`/`baggage` automatically on every hop
  it fronts — no code in either service does anything for the
  edge-gw → catalog-svc leg beyond being an ordinary reverse proxy (which
  forwards all headers by default).
- `catalog-svc`'s call to the payments stub is a genuinely new outbound
  request (different body, different destination), so it's the one place
  that explicitly copies `traceparent`/`baggage` from the inbound request —
  see `forwardTraceHeaders` in `services/catalog-svc/main.go`. That's the
  entire "propagation contract" mentioned in design.md §8: two headers, no
  ensemble dependency.
- Downstream calls always target the *proxy* port (`9081` for catalog, not
  its real port `8081`) — that's what puts the hop in the trace at all.

## Layout

```
sample/
├── ensemble.yaml              # the reference config for this slice
├── seeds/baseline.sql         # a few starter products (idempotent)
└── services/
    ├── edge-gw/                # standalone Go module — entry + auth stub
    └── catalog-svc/            # standalone Go module — Postgres CRUD
```

Each service is its own Go module, deliberately outside the repo's
`go.work` — that mirrors how a real company's services would actually be
separate repos, and it means neither service depends on anything in this
monorepo (`core`, `ensemble`) to run. `ensemble.yaml`'s `build:` step
sets `GOWORK=off` because Go otherwise auto-detects the parent `go.work`
and refuses to build a module that isn't listed in it.
