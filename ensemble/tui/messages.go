package tui

import (
	"time"

	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/ensemble/orchestrator"
	"github.com/caribou-crew/ensemble/ensemble/server"
)

// pollInterval is how often the Services/Latency/Profiles panels refresh
// their polled state — matches dashboard/ensemble-ui's useHealthPoll
// default (design.md). A var, not a const, so tests can shorten it instead
// of a tick-driven test taking multiple real seconds.
var pollInterval = 3 * time.Second

// tickMsg drives the poll loop for whichever panel is active; see
// model.go's scheduleTick.
type tickMsg time.Time

// statusMsg carries the result of a GET /api/status poll.
type statusMsg struct {
	resp StatusResponse
	err  error
}

// topologyMsg carries the result of a GET /api/topology poll. The Services
// panel only reads this for gateway nodes — the sole category with no
// ServiceState (gateways are static listeners, not orchestrator-supervised
// nodes) and so otherwise invisible to a panel driven off statusMsg alone.
type topologyMsg struct {
	resp server.TopologyResponse
	err  error
}

// latencyMsg carries the result of a GET /api/latency poll or a
// latency-rule mutation (arm-all/reset), both of which return the same
// {"rules": [...]} shape.
type latencyMsg struct {
	resp LatencyListResponse
	err  error
}

// profilesMsg carries the result of a GET /api/profiles poll or a
// profile up/down mutation, both of which return orchestrator.ProfilesState.
type profilesMsg struct {
	resp orchestrator.ProfilesState
	err  error
}

// actionMsg reports the outcome of a fire-and-refresh action (service
// restart/flip/seed) — action names the verb for the footer message, err
// is non-nil on failure. A successful action also carries the endpoint's
// own updated ServiceState so the table can reflect it before the next poll.
type actionMsg struct {
	action  string
	service string
	state   orchestrator.ServiceState
	err     error
}

// serviceLogMsg carries the result of a GET /api/services/{name}/logs
// fetch for the Services panel's log view — see servicesPanel.applyLog.
type serviceLogMsg struct {
	service string
	content string
	err     error
}

// hopMsg is one hop delivered by the traffic panel's SSE subscription.
type hopMsg struct {
	hop trace.Hop
}

// hopStreamClosedMsg is sent if the traffic subscription's channel closes
// (only happens when its ctx is canceled — StreamTraffic reconnects on its
// own for every other failure mode).
type hopStreamClosedMsg struct{}
