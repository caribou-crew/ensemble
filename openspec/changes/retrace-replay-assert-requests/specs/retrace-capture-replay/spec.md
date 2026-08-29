## ADDED Requirements

### Requirement: Request-side replay assertion
`retrace replay --ref <flow> --assert-requests -- <command>` SHALL, in
addition to serving recorded responses, record every request the client
made and compare it against the reference bundle's recorded requests using
the same pairing and field/header diff logic `retrace diff`'s wire plane
uses (bucketed by method + normalized path + normalized query, diffed under
the project's configured `wire_rules` and `query_ignore`). A request that
matched a recorded exchange more than once, or that matched but carries a
request field or header the recording did not declare, SHALL be reported as
a deviation and, when it exceeds the configured `gates.wire.budget_pct`
(or any deviation at all when no budget is configured), SHALL fail the
run with the same exit code an unmatched-call miss already uses. Without
`--assert-requests`, replay's behavior, report shape, and exit codes SHALL
be unchanged.

#### Scenario: Call-count drift caught
- **WHEN** a code change makes a flow call an endpoint 5 times where the
  reference recorded it once, and every call still matches that one
  recorded exchange under `Match`'s subset rule
- **THEN** `retrace replay --assert-requests` reports 4 surplus calls in
  `extra` and exits non-zero, where a plain `retrace replay` would report
  every call served and exit 0

#### Scenario: New request header caught
- **WHEN** a client starts sending a header or body field on a request
  whose path still matches a recorded exchange, and the recording never
  declared that field
- **THEN** `retrace replay --assert-requests` reports the field as a
  request-side deviation and exits non-zero, where a plain `retrace replay`
  matches the call and exits 0

#### Scenario: A genuine miss is not double-reported
- **WHEN** a request matches no recorded exchange at all (an ordinary miss)
- **THEN** it is reported once, through the existing miss mechanism, and is
  not additionally listed as a request-side deviation

#### Scenario: Flag absent leaves replay unchanged
- **WHEN** `retrace replay` runs without `--assert-requests`
- **THEN** the report carries no `extra` or `requestDiff` field, and every
  exit code matches today's behavior exactly
