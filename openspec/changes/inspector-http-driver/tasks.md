# Tasks: inspector-http-driver

## 1. Config

- [x] 1.1 `Database.URL`, `Database.Headers` fields.
- [x] 1.2 `"http"` added to `validDatabaseTypes`; validation requires `url`
      when `type: http`. Tests: valid http entry, missing url rejected.

## 2. Inspector driver

- [x] 2.1 `inspector.NewHTTPDriver(baseURL, headers)` implementing `Driver`:
      `Tables`/`Rows`/`Fingerprint` as GETs against `{base}/tables`,
      `{base}/rows?table=&limit=&offset=`, `{base}/fingerprint?table=`, with
      `headers` applied to every request. Tests against `httptest.Server`:
      happy path for all three, unknown-table 404, non-2xx surfaces as error,
      context cancellation propagates.

## 3. Wiring

- [x] 3.1 `buildInspector`'s type switch: `case "http"`.

## 4. Docs

- [x] 4.1 README config reference for `type: http`, `url`, `headers`.
- [x] 4.2 Worked example pointing at local-stack's cardco-go reference
      implementation (`/ensemble-inspect/*`).
