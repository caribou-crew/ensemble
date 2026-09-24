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

#### Scenario: An allowlisted repeat is tolerated but still reported
- **WHEN** a flow calls an endpoint more times than the reference recorded,
  every surplus call still matches that endpoint's method + normalized path
  (a `"repeat"`, not a `"new"` call), and a `wire_repeats:` entry in
  `retrace.yaml` names that method/path with a `max_extra` at or above the
  surplus count
- **THEN** `retrace replay --assert-requests` exits 0, `extra` still lists
  every surplus call (each carrying `kind: "repeat"` and a `tolerated`
  note), and `requestDiff.toleratedRepeats` counts them

#### Scenario: An over-budget repeat still fails
- **WHEN** the same surplus count exceeds the matching `wire_repeats`
  entry's `max_extra`
- **THEN** none of that endpoint's surplus calls are tolerated (the budget
  is evaluated for the whole group, not call by call) and the run fails
  exactly as it would with no `wire_repeats` entry at all

#### Scenario: `wire_repeats` never excuses a new endpoint
- **WHEN** a call's method + normalized path was never recorded by the
  reference at all (a `"new"` call), regardless of any `wire_repeats`
  entry naming that exact method and path
- **THEN** the call is never tolerated by `wire_repeats` — a genuinely new
  endpoint fails today through the pre-existing miss mechanism before
  `wire_repeats` is ever consulted
