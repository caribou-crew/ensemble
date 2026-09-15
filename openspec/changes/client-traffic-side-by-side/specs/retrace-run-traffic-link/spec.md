## Purpose

Makes a `retrace run` and the ensemble hops it produced one navigable
object: the run's manifest records which control plane it attached to and
the session it registered, so the retrace dashboard can open that run's
traffic in the ensemble dashboard, and the ensemble Traffic view can be
scoped to one run by URL.

## ADDED Requirements

### Requirement: An ensemble-attached run records its control-plane link
`runs.Manifest` SHALL carry an optional `ensemble` object recording the
control-plane base URL the run attached to and the session id it registered
with that control plane. It SHALL be written only for a run captured in
ensemble-attached mode, and SHALL be absent — not zero-valued — for a
standalone run and for every manifest written before this field existed.
The session id SHALL be the value actually registered with ensemble, not a
value re-derived by a reader.

#### Scenario: An attached run carries the link
- **WHEN** `retrace run` captures a flow against a running ensemble stack
- **THEN** its manifest's `ensemble` object records that control plane's
  base URL and the session id registered with it

#### Scenario: A standalone run carries no link
- **WHEN** `retrace run` captures a flow in standalone mode
- **THEN** its manifest has no `ensemble` object, rather than one with
  empty fields

#### Scenario: An older manifest decodes as unlinked
- **WHEN** a manifest written before this field existed is read
- **THEN** it decodes with no ensemble link and is not treated as attached
  to an empty control plane

### Requirement: The retrace dashboard links a run to its ensemble traffic
For a run whose manifest carries an ensemble link, the retrace dashboard
SHALL offer navigation to that run's traffic in the ensemble dashboard,
addressed by the recorded control-plane URL and session id. For a run with
no link, no such navigation SHALL be offered.

#### Scenario: A linked run offers the traffic link
- **WHEN** a run captured against an ensemble stack is viewed
- **THEN** the view offers a link to that run's hops in the ensemble
  dashboard

#### Scenario: A standalone run offers nothing to click
- **WHEN** a run captured in standalone mode is viewed
- **THEN** no ensemble traffic link is shown

### Requirement: The ensemble Traffic view can be scoped to one run by URL
The Traffic view SHALL accept a run/session identifier in URL state and
open scoped to that run's hops, using the session filtering it already has,
so a link handed over from retrace lands on that run's traffic directly.

#### Scenario: Opening a run's traffic from a link
- **WHEN** the Traffic view is opened with a session identifier in URL
state
- **THEN** it opens filtered to that session's hops, without further
  interaction

#### Scenario: An unknown run scopes to nothing rather than to everything
- **WHEN** the Traffic view is opened with a session identifier that has no
  hops in the window
- **THEN** it shows no hops under that scope rather than falling back to
  the unfiltered stream
