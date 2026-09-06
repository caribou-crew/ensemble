# Agent observability

## Requirements

Expose read-only, bounded request search and trace explanation through REST first, and through `ensemble mcp` using MCP stdio. Preserve existing traffic, trace, CLI, and hop-schema contracts. No new Go dependencies.

- `GET /api/observability/requests`: optional `service`, `path`, `errorsOnly`, `minDurationMs`, and `limit` (default 20, maximum 100). Return recent matching hop summaries with sequence/trace IDs, endpoints, method/path, status, duration and capture-quality evidence. Exclude payload bodies and headers. Bound individual strings and disclose truncation. Invalid query values return 400.
- `GET /api/observability/traces/{traceId}`: optional `limit` (default 50, maximum 200); deterministic explanation including observed hops, slowest/error hop references, absent parent references, incomplete streaming/redaction/unsupported evidence, and recorder loss counters. Empty trace is not a healthy trace. The source is the current bounded in-memory ring; never claim complete historical coverage. All findings must refer to observed evidence and must not invent causality.
- Response scope explicitly states live-ring coverage and limits. Capture completeness is unknown unless proven; zero counters alone cannot establish completeness. Mark row/field truncation explicitly.
- `ensemble mcp [--api-url URL]`: stdio JSON-RPC, initialization/version negotiation, ping, tools/list and tools/call. The only tools are `ensemble_requests` and `ensemble_explain_trace`, mapping to the two GET endpoints. Tool annotations advertise read-only, non-destructive behavior. Return structuredContent plus a JSON text content item. No arbitrary endpoint/URL/shell tools and no doctor tool.
- Bound incoming messages and HTTP responses, set finite HTTP timeouts, disable redirects, validate tool arguments, preserve request IDs exactly, and keep non-protocol output off stdout. Support MCP protocol 2025-11-25 and compatible 2025-06-18/2024-11-05 negotiation. Unknown/malformed requests return protocol errors; REST failures become tool errors. No writes or client configuration edits occur merely by starting MCP.

## Verification

Real server tests exercise filters, empty evidence, truncation, redaction/stream quality, loss counters and unrelated trace exclusion. MCP tests drive newline-delimited JSON-RPC over streams against a real HTTP test server, verifying initialization, discovery, calls, invalid parameters, HTTP errors, notification silence and read-only routing. Compiling mutations must demonstrate the important assertions fail.
