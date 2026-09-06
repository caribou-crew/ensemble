# Codebase audit — September 6, 2026

This audit focused on local FE → BFF → BE observability, trustworthy image/wire comparisons and strict replay, Playwright/Maestro integration, and evidence that an LLM can consume through the existing APIs. The starting revision was `813075a`. The repairs below are in the working tree; no commit, release, or deployment was made.

The largest risks found were incorrect evidence and misleading success: numeric values could change during capture or collapse during comparison, incomplete recordings could compare cleanly, and JSON that could not be redacted could retain credentials while the capture remained trusted. These paths now reject or visibly degrade the affected evidence.

**Implemented repairs**

| Area | Reproduction and resulting behavior |
| --- | --- |
| Capture redaction and trust | Compressed JSON and JSON cut off by the capture limit could bypass structural redaction. With body redaction enabled, uninspectable encoded/truncated JSON now loses its captured bodies and carries a redaction failure. Actual forwarding remains unchanged. Session and capture assessments become degraded, so normal reference acceptance refuses the recording. Explicit existing force/opt-out behavior remains available. |
| Attached and external hop sources | Their in-memory chains could retain the original unsanitized hops even after sanitized copies reached disk. They now retain the successfully written, sanitized chain. Attached wire/full-chain output each redact the original input, avoiding double encryption. External redaction failures contribute negative trust evidence without pretending that external clocks or call counts prove local capture completeness. |
| JSON precision | Capture redaction/decryption, structural diff, request pairing, and strict replay could round large IDs through `float64`. Exact decimal handling now distinguishes adjacent large IDs and tiny nonzero decimals while preserving equivalence such as `1`, `1.0`, and `1e0`. The integer matcher handles exact JSON numbers beyond signed 64-bit range without treating tiny fractions as zero. Exponents remain compact rather than allocating exponent-sized strings. |
| Comparison integrity | Diff previously discarded corrupt-line counts and could treat missing streams as empty even when manifests recorded calls. Comparison now rejects corrupt records and manifest-count shortfalls in either wire or provider hops. The permissive storage reader remains available for inspection. Existing CLI error handling reports inability to compare. |
| Proxy forwarding and trace stitching | All fields nominated by all `Connection` header lines are stripped in both directions. Gateway rewrites correctly escape decoded path characters such as `%`, `?`, and `#`, preserving original escaping for unchanged paths. An invalid incoming `traceparent` no longer suppresses the configured correlation-header fallback or invents an inbound parent. |
| Traffic history | Streaming hops can be persisted after later sequence numbers. Pagination now selects the highest matching sequence numbers using a bounded min-heap rather than assuming append order is sequence order. New recordings resume after the maximum sequence across the current log and retained generations. Startup terminates an unterminated final line before appending, preserving old bytes and including the delimiter in rotation accounting. |
| Session export | An unreadable or corrupt history file previously yielded a successful, incomplete HAR export. Export now returns an error when it cannot establish complete readable history from the available source. Valid exports retain their existing format. |
| Playwright checkpoints | Replacing a named screenshot after turning trimming off could leave the previous `.trim` sidecar active. Replacement now removes the stale sidecar for both explicit false and omitted trim options. |
| Maestro and CI | The documented native `runScript` path invoked a Node ESM script that neither Maestro JavaScript engine could execute. A separate ES5 native helper now uses Maestro's HTTP API; the Node CLI remains available. Documentation passes the marker URL through Maestro's environment explicitly. CI attachment examples now run inside the retrace child environment, preserve the test exit status, and use explicit artifact paths. |
| CLI test cost | Subprocess tests repeatedly compiled the same CLI. They now share one lazily built immutable executable per suite while retaining separate processes, environments, directories, and exit-code assertions. No production dependency was added. |

Relevant implementation entry points are `core/trace/{redact,json,decrypt_body,ctx}.go`, `core/proxy/{proxy,recorder,session}.go`, `ensemble/cmd/ensemble/hop_sequence.go`, `ensemble/server/traffic_history.go`, `retrace/capture/`, `retrace/internal/jsonbody/`, `retrace/diff/{summary,wire}.go`, `retrace/replay/`, and the two runner adapters. Regression tests sit beside the affected packages.

**Verification completed**

The final source passed all repository Go race tests and vet checks, and all 575 JavaScript tests passed. The two optional Maestro engine tests were skipped by the general suite and then run explicitly: both passed with the installed Rhino and Graal engines.

```sh
go test -race ./core/... ./ensemble/... ./retrace/...
go vet ./core/... ./ensemble/... ./retrace/...
pnpm -r --if-present test
git diff --check
```

Regressions were demonstrated before the corresponding fixes. Deliberate compiling mutations verified that the new assertions detect the affected failures, including lost precision, incomplete evidence, missing trust degradation, stale screenshot metadata, history ordering, sequence reuse, and restart line/rotation handling. Go overlays kept mutation experiments separate from the working source. Reviews of the comparison, adapter, and history changes produced follow-up repairs before final verification.

A real Chromium flow recorded two requests and one 900 × 600 checkpoint on each of two standalone captures. It reused a checkpoint name with trimming enabled and then omitted, checking that the stale sidecar disappeared. The structured diff reported `verdict: pass`, both capture statuses `ok`, zero changed pixels, two paired wire calls, and no changed/missing/extra calls or violations. Both pixel and wire budgets were zero. The only suppression was the built-in response `Date` matcher. After accepting the synthetic reference and stopping the upstream, strict replay served both exchanges with zero misses and a successful browser test exit.

The Go suite includes the existing real HTTP three-hop chain integration. Native Maestro helpers were also exercised against the Go marker endpoint. This audit did not run the complete Docker-based sample stack or an actual mobile-device screenshot flow; the standalone browser smoke does not prove those environments work end to end.

**Contract decisions and remaining limits**

Public CLI flags, REST/SSE shapes, the shared hop schema, and stored artifact schemas were retained. Protective error behavior is intentionally stricter: degraded redaction cannot silently become a normal accepted reference, incomplete comparison inputs cannot produce a clean comparison, and unreadable session exports cannot silently return a partial success. `RecorderOpts.InitialSeq` is additive and its zero value retains the existing initial sequence behavior. Generic rotating-file byte semantics are unchanged.

- **Exact commit selection needs a decision.** `runs.FindRun` explicitly resolves full or short SHA selectors using the run-directory suffix's first seven characters. A different full SHA with that prefix can therefore select a recording. Changing this documented behavior was deferred. Prefer an opt-in strict selector that checks the full manifest SHA, with an explicit decision on legacy recordings lacking it.
- **Custom correlation IDs need a decision.** The configured correlation header's raw value is currently adopted as `TraceID`. Arbitrary values can consequently produce a non-W3C `traceparent`. Separating a valid W3C trace ID from the original correlation value would improve interoperability but changes identity semantics and likely needs an additive field plus a migration policy.
- **Old recordings are not repaired.** Previously captured encoded/truncated JSON may already contain credentials, and the existing acceptance scanner can miss those opaque bodies when an old manifest claims the capture is healthy. Re-record affected candidates before relying on or promoting them. JSON field redaction does not cover arbitrary plain text, malformed JSON, or mislabeled opaque formats.
- **Historical storage has limits.** Existing duplicate sequence numbers are preserved. The history API and session export still consult the current history file rather than all rotated generations; startup scans retained generations only to avoid reusing sequence numbers. Truly complete retained-history queries need a defined generation/cursor contract.
- **Legacy evidence remains weaker.** Old absent/zero stream counts retain their compatibility behavior; they cannot prove a count shortfall the way an explicit recorded count can. No retroactive claim of capture completeness was added.

**Recommended next work**

1. **Read-only LLM observability tools.** Put a small MCP layer over the existing REST/SSE APIs: find a request, expand its hop chain, identify slow/error hops, and return a bounded explanation with hop IDs and capture-quality evidence. Build on the shared trace schema and API-first model. Keep tool results explicit about observed versus missing evidence.
2. **An explicit migration comparison workflow.** Add per-side roots/commands, resolved repository and full-commit identities, and configuration fingerprints to a comparison invocation. Cross-root comparison already exists and correctly refuses ambiguous selectors; explicit sides would make two checkouts of the same app much easier to compare reliably.
3. **A probe-based stack doctor.** Send a controlled request through the configured chain and report which services actually propagate trace/session context and route through their proxies. This would make missing-hop diagnosis concrete before running a long E2E flow.
4. **Measured resource controls.** Add a byte budget and visible pressure metrics to the existing bounded recorder queue, then benchmark under stalled-disk and large-body workloads. Its 4,096-hop capacity bounds item count but permits substantial queued body memory. Profile history scans before choosing an index.
5. **Controlled failure injection beyond latency.** Extend the current latency use case with scoped HTTP failures, disconnects, and truncated responses for resilient-client tests, with visible configuration and recorded evidence of which fault was applied.

The first three are the closest fit to the stated day-to-day use cases. They are proposals, not silently introduced product behavior.
