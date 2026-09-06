# Probe the local proxy chain

Run the doctor after starting the stack, using a GET path that exercises the calls you want to inspect:

```sh
ensemble doctor --target bff --path /api/catalog \
  --expect be,provider --timeout 5s --json
```

This sends one GET through the configured `bff` proxy and records the resulting calls under fresh trace/session context. It checks that `bff`, `be`, and `provider` were observed and that the recorded chain preserved trace and session linkage. Expected target names refer to services or gateways configured in this stack. Use a path that is safe for your application to call with GET; the doctor really executes the request.

The same operation is available through REST:

```sh
curl -X POST http://127.0.0.1:4700/api/doctor \
  -H 'Content-Type: application/json' \
  -d '{"target":"bff","path":"/api/catalog","expect":["be","provider"],"timeoutMs":5000}'
```

The default timeout is 5 seconds, with a 30-second maximum. The target must name a configured proxy, and the path must be an origin-form path beginning with `/`. Arbitrary upstream URLs are not accepted. Redirects are not followed and failed probes are not retried. Temporary recording sessions are ended after the bounded observation window, including on errors and cancellation.

The JSON result distinguishes:

- `pass`: the request succeeded, the target and explicitly expected services were observed with linked context, and the checked evidence has no known loss or incompleteness.
- `fail`: the request or an explicit expectation failed, or the observed context is missing or unlinked.
- `inconclusive`: the captured evidence cannot support a pass, for example because no attributable hops arrived, a stream remained open, or capture quality degraded.

The CLI exits 0 for pass, 1 for fail/inconclusive, and 2 for invalid arguments or inability to run the check. Inspect the result's reasons, propagation evidence and limitations rather than relying on the exit code alone.

Without `--expect`, the doctor checks only the path it actually observed. A configured dependency need not execute for every request, so the doctor does not infer that every configured edge was exercised. An unobserved expected service is not proof of which component bypassed its proxy or dropped context. Calls made later than the observation window are outside the result's coverage.

The operation records an annotation in the control plane and ordinary probe hops in the recorder. Existing redaction applies during capture. The result contains compact metadata and evidence references rather than request/response bodies. The doctor is intentionally separate from the read-only MCP tools.
