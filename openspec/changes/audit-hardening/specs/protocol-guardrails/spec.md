## ADDED Requirements

### Requirement: Streaming responses flush through the proxy
A proxied response identified as streaming — `Content-Type:
text/event-stream`, or chunked transfer with no `Content-Length` — SHALL
have each upstream write flushed to the client immediately. Non-streaming
responses keep current behavior.

#### Scenario: SSE through the proxy
- **WHEN** an upstream sends SSE events at 1s intervals through a proxied
  service
- **THEN** the client observes each event within the same second it was
  sent, not batched at stream end

### Requirement: Streaming hops are visible while open
A streaming hop SHALL be recorded when response headers arrive, marked
`streaming: true` with its captured-so-far body, and finalized in place
(duration, final body, truncation) when the stream closes. Subscribers
SHALL receive an update event for the finalization, keyed by the hop's
existing `seq`.

#### Scenario: Long-lived stream appears in Traffic
- **WHEN** an SSE stream has been open for a minute
- **THEN** `GET /api/traffic` already contains its hop with
  `streaming: true` and no final duration

#### Scenario: Stream closes
- **WHEN** the stream ends
- **THEN** the same `seq` is updated with `doneMs` and the final body, and
  an SSE `hop.updated` event is emitted

### Requirement: Unsupported protocols refuse loudly and visibly
A request bearing `Upgrade: websocket` (or an `Upgrade` token in
`Connection`) SHALL receive `501` with a JSON body naming the limitation,
and SHALL be recorded as a hop with `unsupported: "websocket"`. A request
with `Content-Type: application/grpc*` SHALL likewise receive `501` and
`unsupported: "grpc"`. A capture session containing any such hop SHALL
carry a degraded note naming the protocol. The dashboard SHALL render
these hops with a distinct badge.

#### Scenario: WebSocket attempt
- **WHEN** a client attempts a WebSocket upgrade through a proxied port
- **THEN** it receives an immediate `501` explaining WebSocket is not
  proxied, and the hop appears flagged in the traffic view

#### Scenario: Session verdict
- **WHEN** a retrace recording session contains a `unsupported` hop
- **THEN** the run's capture verdict carries a degraded note naming it

### Requirement: Binary bodies are captured losslessly
A captured body that is not valid UTF-8, or whose content type is a
known-binary family (`image/*`, `application/octet-stream`,
`application/pdf`, `application/protobuf`, `application/grpc*`,
`font/*`), SHALL be stored base64-encoded in a `bodyB64` payload field
(same size cap, `truncated` semantics unchanged) instead of `body`.
Replay SHALL serve the decoded bytes verbatim; diff SHALL compare such
payloads as opaque equal/not-equal. Recordings written before this change
SHALL load unchanged.

#### Scenario: PNG response
- **WHEN** a proxied response is a 40 KB PNG
- **THEN** the hop's response payload carries `bodyB64` (no `body`), and a
  replay HIT returns bytes identical to the original

#### Scenario: Old recording
- **WHEN** a pre-change `wire.jsonl` (string bodies only) is loaded
- **THEN** parsing and replay behave exactly as before
