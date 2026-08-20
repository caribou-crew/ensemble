# adapters

## ADDED Requirements

### Requirement: Thin in-test adapters
The adapters SHALL provide checkpoint screenshots and flow-part
grouping/markers only — via npm packages retrace-playwright, retrace-maestro
(HTTP markers), and retrace-js; all diffing, recording, and serving stays in
the Go binary.

#### Scenario: Playwright fixture
- **WHEN** a Playwright test uses the retrace fixture to mark a checkpoint
- **THEN** a named screenshot and a groups.jsonl entry appear in the active run dir

### Requirement: Env-based handshake
Adapters SHALL discover the active run via env (`RETRACE_RUN_DIR`,
`RETRACE_PROXY_URL`) set by `retrace run`, and SHALL fail loudly if invoked
without them when strict mode is on.

#### Scenario: Missing handshake
- **WHEN** the fixture runs outside `retrace run` in strict mode
- **THEN** the test fails with a message explaining how to invoke retrace
