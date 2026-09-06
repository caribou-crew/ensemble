# Read-only observability for LLMs

Start the stack normally, then configure your MCP client to launch:

```sh
ensemble mcp --api-url http://127.0.0.1:4700
```

A client that accepts a JSON `mcpServers` configuration can use:

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

The executable must be on that client's PATH. Starting the MCP bridge does not start the stack or change the client's configuration. `ENSEMBLE_API` supplies the default API URL when the flag is omitted.

The bridge exposes two tools:

| Tool | Arguments | Result |
| --- | --- | --- |
| `ensemble_requests` | Optional `service`, `path`, `errorsOnly`, `minDurationMs`, `limit` | Recent matching hop metadata, newest sequence first. `service` matches either endpoint; `path` is a substring. Limit defaults to 20, maximum 100. |
| `ensemble_explain_trace` | Required `traceId`; optional `limit` | Observed hops, slowest hop reference, HTTP/error findings, missing parent references and capture-quality limitations. Limit defaults to 50, maximum 200. |

Useful prompts include “find recent slow calls to the orders service” and “explain the failing provider hop in this trace.” Tool results include hop sequence IDs and trace IDs for following up. Timings are inclusive at each proxy: adding nested durations does not yield total request latency. The explanation identifies observed outcomes and context gaps; it does not invent a root cause.

Both tools call existing-origin GET endpoints with the same JSON arguments expressed as query parameters:

```sh
curl 'http://127.0.0.1:4700/api/observability/requests?service=orders&errorsOnly=true&limit=10'
curl 'http://127.0.0.1:4700/api/observability/traces/TRACE_ID?limit=50'
```

These responses omit bodies, headers and raw error text. Strings, row counts and example references are bounded, and truncation is explicit. The source is the current recorder ring: `scope.completeness` is `unknown` or `degraded`, never a certification that an entire historical trace was captured. A missing parent may belong to an external caller or an evicted hop. Recorder loss counters describe persistence over the recorder lifetime; they do not identify which trace lost data.

MCP results include both structured JSON and a text JSON content item. The transport uses newline-delimited stdio JSON-RPC, with diagnostics on stderr, finite HTTP timeouts and fixed read-only endpoint mappings. There are no restart, probe, arbitrary URL, or shell-execution tools. The implementation follows the MCP [stdio transport](https://modelcontextprotocol.io/specification/2025-11-25/basic/transports), [lifecycle](https://modelcontextprotocol.io/specification/2025-11-25/basic/lifecycle), and [tools](https://modelcontextprotocol.io/specification/2025-11-25/server/tools) specifications.
