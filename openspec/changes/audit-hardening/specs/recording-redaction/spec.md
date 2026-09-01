## ADDED Requirements

### Requirement: Default body redaction
When a captured request or response body parses as JSON, the recorder
SHALL redact the value of any object key (case-insensitive, at any
nesting depth) matching the built-in secret-key list already used for
query-string redaction (`access_token`, `password`, `client_secret`, …),
using the active redaction mode (destroy or encrypt). Non-JSON bodies
SHALL be left unmodified. A config MAY opt out via
`redact: { body_defaults: off }`.

#### Scenario: Login response with a token
- **WHEN** a recorded response body is `{"user":"a","access_token":"tok123"}`
- **THEN** the stored body carries a redacted `access_token` value and an
  untouched `user` value

#### Scenario: Nested secret
- **WHEN** a body is `{"data":{"credentials":{"password":"p"}}}`
- **THEN** the `password` value is redacted

#### Scenario: Opt-out
- **WHEN** `retrace.yaml` sets `redact: { body_defaults: off }` and no
  user redact rule matches
- **THEN** bodies are recorded verbatim (headers/query defaults still apply)

### Requirement: Redaction-aware replay matching
A recorded value equal to the destroy-mode redaction sentinel SHALL match
any live value during replay body-subset matching and significant-query
comparison, exposed as a built-in `redacted` matcher. Redacting a request
field SHALL NOT by itself cause a replay miss.

#### Scenario: Redacted request field
- **WHEN** a reference exchange's request body has `"password":"[redacted]"`
  and a live replayed request sends `"password":"hunter2"`
- **THEN** the exchange matches (given all other fields match)

#### Scenario: Redacted query parameter
- **WHEN** the recorded match key's query has `token=[redacted]` and the
  live request sends `token=abc`
- **THEN** the query comparison treats `token` as equal

### Requirement: Accept-time secret scan
`retrace ref accept` (CLI and review-server accept) SHALL scan the staged
bundle's wire exchanges — headers, query strings, and bodies, after
redaction — for likely credentials: non-redacted values under secret-list
keys, JWT-shaped strings, AWS access key ids, and `Bearer`-token headers.
Any finding SHALL refuse the accept, reporting each finding's field path
and a suggested `retrace ref rule` command. `--force` SHALL override,
recording `acceptedWithSecrets: true` in the reference manifest.

#### Scenario: Token in an unredacted body field
- **WHEN** a staged exchange's body contains `"session_key":"eyJhbGciOi..."`
  and no rule redacts it
- **THEN** accept exits non-zero, names `resp.body.session_key`, and the
  reference directory is unchanged

#### Scenario: Forced accept
- **WHEN** the same accept is re-run with `--force`
- **THEN** the reference is promoted and its manifest records
  `acceptedWithSecrets: true`

#### Scenario: Clean bundle
- **WHEN** a staged bundle has no findings
- **THEN** accept proceeds exactly as before this change
