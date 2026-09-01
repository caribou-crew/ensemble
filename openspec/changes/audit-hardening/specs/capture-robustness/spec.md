## ADDED Requirements

### Requirement: Redaction failures degrade, never panic
A redaction failure while recording a hop SHALL record the hop with its
payload bodies dropped and `err` describing the redaction failure. No
code path reachable from `Recorder.Record` may panic (the crypto/rand
entropy guard excepted).

#### Scenario: Broken encrypt invariant
- **WHEN** encrypt-mode redaction fails on a hop
- **THEN** the proxied request completes normally and the hop appears with
  empty bodies and a redaction error note

### Requirement: Recorder disk writes do not serialize requests
Hop persistence SHALL happen outside the recorder's request-path lock via
an ordered, bounded queue. Queue overflow SHALL drop the write (never
block the request) and increment a counter; write errors SHALL increment
a counter instead of being discarded. Both counters SHALL be visible in
`GET /api/status`.

#### Scenario: Slow disk
- **WHEN** disk writes stall
- **THEN** proxied request latency is unaffected, and once the queue
  overflows `status` reports the dropped-write count

### Requirement: Hop rings are byte-budgeted
The in-memory hop ring SHALL enforce a configurable byte budget (default
256 MB) alongside its count cap, evicting oldest hops until under both.

#### Scenario: Many large bodies
- **WHEN** captured hops carry 256 KB bodies at high volume
- **THEN** resident ring memory stays under the budget

### Requirement: No silent hop loss around session end
Hops routed to a session after `End` SHALL be counted, and the count
SHALL appear in the session result/run manifest (`droppedHops`), with any
non-zero value degrading the run's verdict note. Session `Start` SHALL
reject a duplicate session id before binding its edge listener.

#### Scenario: Late hop
- **WHEN** one hop arrives after the session ended
- **THEN** the run manifest reports `droppedHops: 1` and the verdict note
  says so — `verdict: ok` with zero drops remains provably zero

### Requirement: Stub input is bounded and local
The stub engine SHALL cap request-body reads at the configured body cap
(413 beyond it), bind with the same loopback-only enforcement as the
proxy, and both stub and proxy HTTP servers SHALL set a read-header
timeout.

#### Scenario: Oversized POST to a stub
- **WHEN** a 1 GB body is POSTed to a stub route
- **THEN** the stub responds 413 without buffering the full body

### Requirement: Multiple Set-Cookie values survive round-trip
Response payloads SHALL preserve every `Set-Cookie` header value in order
(a `setCookies` list alongside the joined `headers` form), and replay
SHALL emit each as its own header.

#### Scenario: Two cookies recorded, two replayed
- **WHEN** an upstream response sets two cookies
- **THEN** a replay HIT of that exchange emits two `Set-Cookie` headers
  matching the originals in order

### Requirement: Refusals are per-exchange and rule-excludable
A reference bundle exchange refused for truncation, `Content-Encoding`,
or 206/`Content-Range` MAY be excluded from the bundle by a wire rule
(`exclude: true` with a mandatory `why`); the load error for an
unexcluded refusal SHALL name the exchange and print the exact rule
command that would exclude it. A bundle whose only defects are excluded
exchanges SHALL load and serve normally.

#### Scenario: One truncated body
- **WHEN** one exchange's body exceeded the cap and a rule excludes that
  route with a why
- **THEN** replay serves the remaining exchanges; requests matching the
  excluded route miss with the standard explained 501
