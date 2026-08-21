# docker-build-ergonomics

## ADDED Requirements

### Requirement: Streamed build output
A service's `build` command output SHALL be appended to the service's log
file as it is produced, preceded by a header naming the command; a failed
build's error SHALL include the tail of that output.

#### Scenario: Long build is visible
- **WHEN** a build prints lines over several seconds
- **THEN** `.ensemble/run/<name>.log` receives them before the build exits

#### Scenario: Failed build explains itself
- **WHEN** a build exits non-zero after printing a reason
- **THEN** the service is `failed` and `lastErr` contains that reason

### Requirement: docker run passthrough
`docker.args` SHALL be appended verbatim to `docker run` before the image.

#### Scenario: Host gateway alias
- **WHEN** `docker.args: ["--add-host=host.docker.internal:host-gateway"]`
- **THEN** the container is started with that flag
