# Inspect a local request with doctor and MCP

Use ensemble 0.0.34 or newer. Start the sample in one terminal with
`ensemble up -c ensemble.yaml` from `sample/`, and leave it running. Run
these commands in a second terminal, also from `sample/`:

```sh
ensemble ready --timeout 60s
mkdir -p .retrace/demo
ensemble doctor --target public --path /console/products \
  --expect ops,catalog --json > .retrace/demo/doctor.json
```

This exercises `public → ops → catalog`: the gateway rewrites
`/console/products` to the ops BFF's `/admin/products`, and the BFF fetches
`/products` through catalog's proxy. This read-only sample route needs no
Authorization header. The separate `edge` service's browser routes do
require the demo bearer token, so `/products` through `edge` is not an
interchangeable doctor example.

Inspect the JSON, using `jq` or your preferred JSON viewer:

```sh
jq '{verdict, httpStatus, observedTargets, missingExpected, propagation, capture}' \
  .retrace/demo/doctor.json
```

The healthy result has `verdict: "pass"`, HTTP 200, observed targets
`catalog`, `ops`, and `public`, and true trace/session/linkage fields.
Doctor also creates a temporary recording edge; it can appear in `hops`
but is not a fourth application service.

To demonstrate a failed expectation, run:

```sh
ensemble doctor --target public --path /console/products \
  --expect ops,catalog,order --json > .retrace/demo/missing-target.json
```

That command intentionally exits **1**. The HTTP request succeeds, but
`verdict` is `fail` and `missingExpected` contains `order`: the catalog
browse route does not call the order service. If you automate this example,
handle that expected exit explicitly and inspect the JSON. A failed or
inconclusive probe is never a passing topology check.

## Follow the evidence through REST

```sh
api=${ENSEMBLE_API:-http://127.0.0.1:4700}
trace_id=$(jq -r .traceId .retrace/demo/doctor.json)
curl -fsS "$api/api/observability/traces/$trace_id" \
  > .retrace/demo/trace.json
curl -fsS "$api/api/observability/requests?service=catalog&limit=5" \
  > .retrace/demo/requests.json
```

Requests are in `hops`; the trace explanation also contains `findings`,
`slowestHopSeq`, and `scope`. These APIs return bounded metadata, not
bodies, headers, or raw error strings. Their source is the current recorder
ring: `scope.completeness` remains `unknown` or `degraded`. Doctor's bounded
probe verdict does not turn this general-purpose API into proof that all
historical calls are present.

## Make a slow hop easy to identify

The following subshell arms a 750 ms rule for catalog's `/products` path,
records another doctor request, and disarms the rule on exit. It replaces
any existing rule for that exact target/path; use it on the sample's demo
configuration.

```sh
(
  trap 'ensemble latency set --target catalog --path /products --fixed 750 --enabled=false >/dev/null' EXIT
  ensemble latency set --target catalog --path /products --fixed 750 --enabled
  ensemble doctor --target public --path /console/products \
    --expect ops,catalog --json > .retrace/demo/slow.json
  api=${ENSEMBLE_API:-http://127.0.0.1:4700}
  curl -fsS "$api/api/observability/requests?service=catalog&minDurationMs=700&limit=5" \
    > .retrace/demo/slow-requests.json
)
```

The catalog hop records `injectedDelayMs: 750`. Its callers also take
longer while waiting. Durations are inclusive; adding nested hop durations
would double-count the same wait. Inspect `slow.json`'s trace ID through
the trace endpoint to follow the linked calls.

## Give an LLM the same visibility

Configure your MCP client to start this local process while ensemble is up:

```json
{
  "mcpServers": {
    "ensemble": {
      "command": "ensemble",
      "args": ["mcp", "--api-url", "http://127.0.0.1:4700"]
    }
  }
}
```

Try “Show recent catalog calls slower than 700 ms” and then “Explain the
observed hops for this trace ID; identify any injected delay and missing
context.” The client can call `ensemble_requests` and
`ensemble_explain_trace`. Both are read-only. Run doctor and latency changes
in your terminal; the MCP bridge does not expose those mutations.

The walkthrough was exercised against the live brew stack, including the
missing-order expectation, 750 ms injection, REST results, and both tools
through the actual stdio MCP process. See the full
[doctor guide](../../docs/stack-doctor.md) and
[MCP/API guide](../../docs/agent-observability.md) for limits and arguments.
