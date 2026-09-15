## Purpose

Puts two clients' traffic beside each other in the ensemble dashboard on
one shared row axis, so a call that one client makes and the other does not
— a different route, an extra edge hop, a failing path — is visible as a
gap rather than found by reading an interleaved list.

## ADDED Requirements

### Requirement: The Traffic view offers a two-pane side-by-side mode
The Traffic view SHALL offer a side-by-side mode in which a client identity
is selected for the left pane and one for the right, each pane showing only
hops resolving to its client, using the same row rendering as the single
merged table. The mode and both pane selections SHALL be reflected in URL
state so a side-by-side arrangement can be shared or reloaded. Leaving the
mode SHALL return to the merged table with its own filters intact.

#### Scenario: Two clients render in their own panes
- **WHEN** side-by-side mode is entered with `app-legacy` on the left and
  `app-next` on the right
- **THEN** each pane lists only the hops resolving to its own client

#### Scenario: The arrangement survives a reload
- **WHEN** the page is reloaded with the side-by-side URL state present
- **THEN** the view reopens in side-by-side mode with the same two clients
  selected

### Requirement: Both panes share one interleaved row axis
Hops shown across both panes SHALL be merged into one total order by hop
start time, tie-broken by `seq`, and each merged position SHALL occupy one
row slot spanning both panes. A hop SHALL render in its own client's column
at its slot, leaving the opposite column empty at that slot.

#### Scenario: A call made by only one client leaves a gap opposite it
- **WHEN** the left client calls a service that the right client never
  calls
- **THEN** that call occupies a row slot with the left column filled and
  the right column empty at the same slot

#### Scenario: Rows stay in one interleaved order
- **WHEN** the two clients' calls interleave in time
- **THEN** the row slots follow that interleaved order across both panes,
  rather than each pane ordering independently

### Requirement: Hops belonging to neither pane are reported, never silently dropped
Hops in the window that resolve to neither selected client — including
unattributed hops — SHALL NOT be rendered into either pane, and the view
SHALL report how many are being withheld and allow them to be shown.

#### Scenario: Withheld hops are counted
- **WHEN** the window contains hops resolving to a third client and hops
  resolving to no client, while two other clients are selected
- **THEN** the view reports the number of hops not shown in either pane

#### Scenario: An unattributed hop goes to neither pane
- **WHEN** a hop resolves to unattributed
- **THEN** it appears in neither the left nor the right pane, and is
  counted among the withheld hops

### Requirement: Side-by-side uses the existing traffic stream
Entering side-by-side mode SHALL NOT open an additional live-traffic
connection; both panes SHALL be fed by partitioning the single stream the
Traffic view already consumes, so the two panes cannot hold data from
different points in the stream.

#### Scenario: No second stream is opened
- **WHEN** side-by-side mode is entered
- **THEN** the number of open traffic stream connections is unchanged

#### Scenario: Both panes advance together
- **WHEN** new hops for both clients arrive
- **THEN** both panes reflect the same stream position, with no pane
  showing hops the other has not yet accounted for
