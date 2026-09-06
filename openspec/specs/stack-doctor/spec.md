# Probe-based stack doctor

## Requirements

Expose `POST /api/doctor` and a thin `ensemble doctor --target NAME --path /... [--expect a,b] [--timeout DURATION] [--api-url URL] [--json]` client. The probe is explicit and on demand. It sends one bounded GET to a configured service/gateway through its configured proxy. No startup automation, arbitrary upstream URL, redirects, retries, process restarts or configuration mutations.

- Require target and origin-form path. Resolve the target from live stack configuration; reject unknown or non-proxied targets and malformed paths before sending traffic. Accept timeout milliseconds with a finite default (5 seconds) and hard maximum (30 seconds), and optional expected service names validated against configuration.
- Stamp fresh W3C trace context and unique session baggage; use existing capture/session machinery to gather observed evidence and propagation gaps. End any temporary session on every exit path. Capture request/response bodies remain governed by the existing capture redactor; the doctor result itself contains compact metadata only.
- Return HTTP status, bounded timing, trace/session IDs, observed hop references and routes, expected-but-unobserved targets, context propagation evidence, recorder/subscriber loss and capture-quality limitations. Do not infer that a declared dependency executes on this path. A target not observed is unobserved, not proof of which service dropped context.
- Distinguish `pass`, `fail`, and `inconclusive`. Pass requires a successful probe, observed target, all explicitly expected targets, valid linked trace/session context, no known capture loss or degradation, and completed observed hops. Zero hops, missing context, or known capture losses never pass. Without explicit expectations state that the check covers only the observed path and cannot prove every configured dependency was exercised.
- Observe a bounded drain window after the HTTP request to allow recorder delivery; cancel promptly on caller cancellation. Do not report ambient traffic as evidence for the probe. Reuse existing HTTP browser guard and mutation annotation wrapper, and list the endpoint in OpenAPI.
- CLI exit 0 for pass, 1 for fail/inconclusive, 2 for argument errors or inability to run. Preserve existing commands.

## Verification

Real HTTP proxy-chain tests cover trace+baggage propagation, missing baggage, dropped trace context, bypassed expected proxy, HTTP error, timeout/cancellation, non-proxied target, invalid path, redirect refusal and cleanup. Assert JSON evidence and CLI exit behavior. Use deliberate compiling mutations for pass gating and expected-target/context checks.
