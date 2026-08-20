# adapters

## ADDED Requirements

### Requirement: Thin in-test adapters
The adapters SHALL provide checkpoint screenshots and flow-part
grouping/markers only — via npm packages encore-playwright, encore-maestro
(HTTP markers), and encore-js; all diffing, recording, and serving stays in
the Go binary.

#### Scenario: Playwright fixture
- **WHEN** a Playwright test uses the encore fixture to mark a checkpoint
- **THEN** a named screenshot and a groups.jsonl entry appear in the active run dir

### Requirement: Env-based handshake
Adapters SHALL discover the active run via env (`ENCORE_RUN_DIR`,
`ENCORE_PROXY_URL`) set by `encore run`, and SHALL fail loudly if invoked
without them when strict mode is on.

#### Scenario: Missing handshake
- **WHEN** the fixture runs outside `encore run` in strict mode
- **THEN** the test fails with a message explaining how to invoke encore
