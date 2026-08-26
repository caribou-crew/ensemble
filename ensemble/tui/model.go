package tui

import (
	"context"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/caribou-crew/ensemble/core/trace"
)

// tab identifies one of the terminal UI's four panels. Order here is the
// order they appear in the tab bar and the order tab/shift+tab cycles
// through — mirrors dashboard/ensemble-ui's VIEWS list, minus Topology and
// Entities (see design.md's Non-Goals).
type tab int

const (
	tabServices tab = iota
	tabTraffic
	tabLatency
	tabProfiles
	tabCount // sentinel: number of tabs, also used for wraparound math
)

var tabLabels = [tabCount]string{
	tabServices: "Services",
	tabTraffic:  "Traffic",
	tabLatency:  "Latency",
	tabProfiles: "Profiles",
}

// model is the terminal UI's root Bubble Tea model. It owns the four
// panels and routes messages/keys to whichever is active; each panel
// keeps its own state (table rows, filters, last error) so switching tabs
// doesn't lose anything.
type model struct {
	ctx    context.Context
	client apiClient

	active        tab
	width, height int

	services servicesPanel
	traffic  trafficPanel
	latency  latencyPanel
	profiles profilesPanel

	hopCh <-chan trace.Hop

	quitting bool
}

func newModel(ctx context.Context, client apiClient) model {
	return model{
		ctx:      ctx,
		client:   client,
		services: newServicesPanel(),
		traffic:  newTrafficPanel(),
		latency:  newLatencyPanel(),
		profiles: newProfilesPanel(),
		hopCh:    StreamTraffic(ctx, client, 0),
	}
}

func scheduleTick() tea.Cmd {
	return tea.Tick(pollInterval, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// Init kicks off every panel's first load, the traffic stream listener, and
// the poll tick loop.
func (m model) Init() tea.Cmd {
	return tea.Batch(
		fetchStatus(m.client),
		fetchTopology(m.client),
		fetchLatency(m.client),
		fetchProfiles(m.client),
		waitForHop(m.hopCh),
		scheduleTick(),
	)
}

// fetchActive returns the Cmd that refreshes whichever panel is currently
// active — used both on tab switch (so switching tabs doesn't show stale
// data until the next tick) and on every tick. Traffic has no polled
// fetch: it's driven entirely by the SSE stream.
func (m *model) fetchActive() tea.Cmd {
	switch m.active {
	case tabServices:
		return tea.Batch(fetchStatus(m.client), fetchTopology(m.client))
	case tabLatency:
		return fetchLatency(m.client)
	case tabProfiles:
		return fetchProfiles(m.client)
	default:
		return nil
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tea.KeyMsg:
		if cmd, handled := m.handleGlobalKey(msg); handled {
			return m, cmd
		}
		return m, m.updateActivePanel(msg)

	case tickMsg:
		return m, tea.Batch(m.fetchActive(), scheduleTick())

	case statusMsg:
		m.services.applyStatus(msg)
		return m, nil
	case topologyMsg:
		m.services.applyTopology(msg)
		return m, nil
	case actionMsg:
		m.services.applyAction(msg)
		return m, nil
	case latencyMsg:
		m.latency.applyLatency(msg)
		return m, nil
	case profilesMsg:
		m.profiles.applyProfiles(msg)
		return m, nil

	case hopMsg:
		m.traffic.appendHop(msg.hop)
		return m, waitForHop(m.hopCh)
	case hopStreamClosedMsg:
		// Only happens when ctx is canceled (StreamTraffic reconnects on
		// its own for every other failure) — the program is shutting
		// down, nothing more to listen for.
		return m, nil
	}

	return m, m.updateActivePanel(msg)
}

// handleGlobalKey handles quit and tab-switch keys, which apply regardless
// of which panel is active. handled is false for any key a panel should
// see instead (including navigation keys the active panel's table needs).
func (m *model) handleGlobalKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	switch msg.String() {
	case "ctrl+c", "q":
		m.quitting = true
		return tea.Quit, true
	case "tab":
		m.active = (m.active + 1) % tabCount
		return m.fetchActive(), true
	case "shift+tab":
		m.active = (m.active - 1 + tabCount) % tabCount
		return m.fetchActive(), true
	case "1":
		m.active = tabServices
		return m.fetchActive(), true
	case "2":
		m.active = tabTraffic
		return nil, true
	case "3":
		m.active = tabLatency
		return m.fetchActive(), true
	case "4":
		m.active = tabProfiles
		return m.fetchActive(), true
	}
	return nil, false
}

func (m *model) updateActivePanel(msg tea.Msg) tea.Cmd {
	switch m.active {
	case tabServices:
		return m.services.update(msg, m.client)
	case tabTraffic:
		return m.traffic.update(msg)
	case tabLatency:
		return m.latency.update(msg, m.client)
	case tabProfiles:
		return m.profiles.update(msg, m.client)
	}
	return nil
}

func (m model) View() string {
	if m.quitting {
		return ""
	}
	width := m.width
	if width <= 0 {
		width = 80
	}
	height := m.height
	if height <= 0 {
		height = 24
	}

	var bar strings.Builder
	for i := range int(tabCount) {
		t := tab(i)
		style := inactiveTabStyle
		if t == m.active {
			style = activeTabStyle
		}
		bar.WriteString(style.Render(tabLabels[t]))
	}

	contentHeight := max(height-5, 5)
	var body string
	switch m.active {
	case tabServices:
		body = m.services.view(width, contentHeight)
	case tabTraffic:
		body = m.traffic.view(width, contentHeight)
	case tabLatency:
		body = m.latency.view(width, contentHeight)
	case tabProfiles:
		body = m.profiles.view(width, contentHeight)
	}

	footer := helpStyle.Render("tab/shift+tab or 1-4 switch panel · q quit")
	return tabBarStyle.Width(width).Render(bar.String()) + "\n" + body + "\n" + footer
}
