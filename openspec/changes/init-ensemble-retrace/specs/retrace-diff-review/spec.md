# retrace-diff-review

## ADDED Requirements

### Requirement: Pixel diff
Retrace SHALL diff screenshots between two runs (pixelmatch algorithm; coarse
and fine thresholds; masks; uniform-border trim) producing per-checkpoint
verdicts and A/B/overlay/diff images.

#### Scenario: Masked region ignored
- **WHEN** a checkpoint has a configured mask over a clock widget
- **THEN** pixel changes inside the mask do not affect the verdict

### Requirement: Wire diff
Retrace SHALL pair calls between two runs on normalized method+path and diff at
field and header level (changed/only-A/only-B, reorder detection via LIS),
honoring wireIgnore and wireRules shape matchers
(uuid/iso8601/http-date/etag/integer/semver/custom/ignore/exact); rule
violations SHALL exit non-zero.

#### Scenario: Volatile field ruled out
- **WHEN** a response field is covered by an iso8601 rule
- **THEN** differing timestamps in that field produce no diff entry

### Requirement: Hop diff
Retrace SHALL diff the provider-chain hop sets of two runs, reporting added or
removed downstream calls and hopRequire violations as hard gates.

#### Scenario: Extra downstream call flagged
- **WHEN** run B's checkout flow makes one more service-to-service call than run A
- **THEN** the hop diff lists the added call and the summary marks the flow changed

### Requirement: Machine-readable everything
Every diff and summary SHALL be available as JSON with stable shapes and
CI-gating exit codes, sufficient for an LLM to judge a change without parsing
human output.

#### Scenario: Agent gate
- **WHEN** an agent runs `retrace diff --json` after a change
- **THEN** it can read verdicts per checkpoint, wire pairs, and hop deltas from one JSON document

### Requirement: Review queue
`retrace serve` SHALL present a single review queue of flows-with-differences
(worst first, passing collapsed), each item a keyboard-driven screen offering
exactly three verbs: accept (re-bless reference), reject (emit repro bundle),
rule (append a wire-rule from the selected field). The same queue and verbs
SHALL be exposed over REST.

#### Scenario: Rule from the UI
- **WHEN** a reviewer marks a diffed field as volatile with the rule verb
- **THEN** a wire-rule is appended to config and the queue re-evaluates without that noise

#### Scenario: LLM walks the queue
- **WHEN** an agent GETs the queue and POSTs accept on an item
- **THEN** the reference updates exactly as if accepted in the UI

### Requirement: Auxiliary checks retained
Retrace SHALL retain: unexpected ≥400 detection with expectedStatuses,
OpenAPI conformance checking against a provided spec, perf budgets, and
a11y-tree diff (flagged experimental until device-verified).

#### Scenario: Unexpected 500
- **WHEN** any recorded call returns 500 and is not allowlisted
- **THEN** the run is marked failed regardless of pixel/wire results
