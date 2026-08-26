# Design: inspector-http-driver

## The contract a service implements

Three GET routes under a base URL the service owns (config's `url` points at
this base; the service can mount it wherever it likes, e.g.
`/ensemble-inspect`). Shapes mirror `inspector.Driver` and its existing wire
types (`Table`/`Column`) field-for-field, so `NewHTTPDriver` is a thin decode,
not a translation layer.

```
GET {url}/tables
  -> 200 {"tables": [{"name": "cards", "columns": [
        {"name": "token", "type": "string", "nullable": false}, ...
     ]}, ...]}

GET {url}/rows?table=cards&limit=50&offset=0
  -> 200 {"rows": [{"token": "...", "user_token": "...", ...}, ...]}
  -> 404 unknown table

GET {url}/fingerprint?table=cards
  -> 200 {"fingerprint": "<opaque string>"}
  -> 404 unknown table
```

`type`/`nullable` on Column are best-effort metadata for the dashboard's
column headers — a service with no real schema (a Go map, in cardco-go's
case) infers them from its own field types when it builds the response.
`fingerprint` has the exact contract `inspector.Driver.Fingerprint` already
documents: opaque, stable when nothing changed, different when something
did — a service can hash its own row count + last-modified timestamp, or
just marshal-and-hash the rows, whichever is cheaper for it.

Any non-2xx from the service (other than 404 for an unknown table) surfaces
to the dashboard as a driver error, same as a postgres/mysql connection
failure today — `Tables`/`Rows`/`Fingerprint` on the Go side just wrap
`net/http` + `encoding/json`, no retry/circuit-breaking beyond what
`http.Client`'s default transport already gives for free.

## Config

```yaml
databases:
  cardco-go-inspect:
    type: http
    url: http://127.0.0.1:4281/ensemble-inspect
    headers:
      Authorization: "Basic YWRtaW5fY29uc3VtZXI6bWFycWV0YQ=="
    services: [cardco]   # existing Database.Services field — ties this
                         # entry's lifecycle/health to the cardco service,
                         # same as it already does for a real DB
```

`services` already exists on `Database` (used today to associate a
provisioned DB container with the services that depend on it); `type: http`
doesn't need a container at all, so this field's only remaining job for an
http-typed entry is documentation/grouping in the dashboard — no orchestrator
change required there.

`url` is required when `type: http` (validation error otherwise, matching
the existing pattern for e.g. a dynamodb entry needing a resolvable port).
`headers` is optional and defaults to none.

## Go changes

`ensemble/config/config.go`:
```go
type Database struct {
    ...
    URL     string            `yaml:"url"`     // type: http only
    Headers map[string]string `yaml:"headers"` // type: http only, sent on every request
}
```

`ensemble/config/validate.go`: add `"http"` to `validDatabaseTypes`; when
`db.Type == "http"`, require `db.URL != ""`.

`ensemble/inspector/httpdriver.go` (new, alongside `postgres.go`/`mysql.go`/
`dynamo.go`):
```go
type HTTPDriver struct {
    baseURL string
    headers map[string]string
    client  *http.Client
}

func NewHTTPDriver(baseURL string, headers map[string]string) *HTTPDriver { ... }

func (d *HTTPDriver) Tables(ctx context.Context) ([]Table, error)         { /* GET {base}/tables */ }
func (d *HTTPDriver) Rows(ctx context.Context, table string, limit, offset int) ([]map[string]any, error) { /* GET {base}/rows?... */ }
func (d *HTTPDriver) Fingerprint(ctx context.Context, table string) (string, error) { /* GET {base}/fingerprint?... */ }
```
Connects lazily like the dynamo driver (a plain `http.Client` wrapper,
nothing to dial up front) — safe to register during `buildInspector` even
before the backing service is healthy, same guarantee the doc comment above
`buildInspector` already states for the other three types.

`ensemble/cmd/ensemble/cmd_up.go`'s `buildInspector`:
```go
case "http":
    insp.Register(name, inspector.NewHTTPDriver(db.URL, db.Headers))
```

## Why not a plugin binary

The original ask framed this as "an adapter we plug in to ensemble.yaml" —
a separate process ensemble shells out to or loads. That would need new
process-lifecycle/plugin-discovery machinery in the orchestrator for
something `inspector.Driver`'s existing three-method shape already covers
over HTTP. Any service can add three JSON GET routes without touching
ensemble's binary at all; ensemble only needs one new `Driver` implementation
that speaks that contract, same tier of change as adding a fourth SQL
dialect. If a future adapter genuinely needs to run out-of-process (wrapping
something with no HTTP surface of its own — a CLI tool, a binary log format),
that is a bigger, separate proposal, not this one.

## Reference implementation (local-stack, not this repo)

`cardco-go` mounts the contract at `/ensemble-inspect/*`, guarded by the same
Basic auth (`admin_consumer`/`cardco`) every other cardco-go route already
requires. Tables: `users`, `accounts`, `cards`, `cardProducts`,
`digitalWalletTokens`, `transactions` — each a JSON round-trip
(`marshal`-then-`unmarshal` into `map[string]any`) of the same structs the
v3 REST API already returns, so the inspector shows exactly the fields a
caller would see over the wire, including the new deterministic tokens.
`fingerprint` hashes the marshaled rows with `fnv`. This exists today as
local-stack's own stopgap (cardco-go serves the contract; nothing in ensemble
proper consumes it yet) and becomes the first real adopter once `type: http`
lands here.
