## MODIFIED Requirements

### Requirement: Redaction at capture
The system SHALL redact sensitive headers (authorization, cookie,
set-cookie, dpop, plus a user-configured list) at capture time, before any
persistence or streaming. A user-configured key SHALL support three modes
— `destroy` (the built-in behavior for headers, and the default when a
mode is not specified), `encrypt` (field-level AES-256-GCM, key material
never persisted in plaintext), and `display` (a capture-time no-op) — with
`destroy` remaining a literal `[redacted]` marker, unchanged from prior
behavior.

#### Scenario: Secret never touches disk
- **WHEN** a proxied request carries an Authorization header
- **THEN** the persisted and streamed hop shows a redaction marker, not the
  value

#### Scenario: Encrypted field carries a versioned, unrecoverable-without-key marker
- **WHEN** a body field configured `mode: encrypt` is captured with a team
  key present
- **THEN** the persisted value is `$enc:v1:<base64 nonce+ciphertext>`, and
  no plaintext or unwrapped data key is ever written to disk
