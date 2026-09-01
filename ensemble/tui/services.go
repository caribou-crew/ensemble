package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/caribou-crew/ensemble/ensemble/orchestrator"
	"github.com/caribou-crew/ensemble/ensemble/server"
)

// servicesPanel is the Services tab: a live table of every node's health
// (GET /api/status, polled) with restart/flip/seed actions bound to keys,
// the terminal analog of the web dashboard's header health strip plus its
// per-service controls.
type servicesPanel struct {
	table    table.Model
	cols     []table.Column // ideal (untruncated) column widths — see fitColumns
	services []orchestrator.ServiceState
	// gateways holds "gateway"-category topology nodes, appended as
	// read-only rows after services — gateways are static listeners with no
	// ServiceState, so they never appear in a GET /api/status poll.
	gateways []server.TopologyNode
	status   string // last action/error, shown in the footer
	loading  bool
	// logService, when non-empty, names the service whose log tail the
	// panel is showing in place of the table (`l` opens, esc closes);
	// logContent is the last fetched tail, re-fetched on every poll tick
	// so the view follows the file.
	logService string
	logContent string
}

// Column order is most- to least-important: on a narrow terminal,
// fitColumns drops from the right, so Service/Status survive longest.
func newServicesPanel() servicesPanel {
	cols := []table.Column{
		{Title: "Service", Width: 22},
		{Title: "Status", Width: 12},
		{Title: "Placement", Width: 10},
		{Title: "Variant", Width: 14},
		{Title: "Port", Width: 6},
	}
	t := table.New(table.WithColumns(cols), table.WithFocused(true), table.WithHeight(12))
	return servicesPanel{table: t, cols: cols, loading: true}
}

// fetchStatus polls GET /api/status.
func fetchStatus(client apiClient) tea.Cmd {
	return func() tea.Msg {
		resp, err := client.Status(context.Background())
		return statusMsg{resp: resp, err: err}
	}
}

// fetchTopology polls GET /api/topology, the Services panel's only source
// of gateway nodes.
func fetchTopology(client apiClient) tea.Cmd {
	return func() tea.Msg {
		resp, err := client.Topology(context.Background())
		return topologyMsg{resp: resp, err: err}
	}
}

func (p *servicesPanel) applyStatus(msg statusMsg) {
	p.loading = false
	if msg.err != nil {
		p.status = msg.err.Error()
		return
	}
	p.setServices(msg.resp.Services)
}

// applyTopology updates the gateway rows only; a topology error is silently
// dropped rather than surfaced as the panel status, since /api/status
// already succeeding means the panel is otherwise healthy and gateways are
// a config-derived addition to it, not its primary content.
func (p *servicesPanel) applyTopology(msg topologyMsg) {
	if msg.err != nil {
		return
	}
	gateways := make([]server.TopologyNode, 0, len(msg.resp.Nodes))
	for _, n := range msg.resp.Nodes {
		if n.Category == "gateway" {
			gateways = append(gateways, n)
		}
	}
	sort.Slice(gateways, func(i, j int) bool { return gateways[i].Name < gateways[j].Name })
	p.gateways = gateways
	p.rebuildRows()
}

func (p *servicesPanel) setServices(services []orchestrator.ServiceState) {
	sorted := append([]orchestrator.ServiceState(nil), services...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	p.services = sorted
	p.rebuildRows()
}

// rebuildRows renders services followed by gateways (each internally
// sorted by name, rather than interleaved) into the table. Gateway rows use
// "—" for the columns that don't apply — they have no placement, variant,
// or lifecycle port the way a supervised service does.
func (p *servicesPanel) rebuildRows() {
	rows := make([]table.Row, 0, len(p.services)+len(p.gateways))
	for _, s := range p.services {
		port := ""
		if s.Port > 0 {
			port = fmt.Sprintf("%d", s.Port)
		}
		rows = append(rows, table.Row{s.Name, serviceStatusCell(s), s.Placement, s.Variant, port})
	}
	for _, g := range p.gateways {
		rows = append(rows, table.Row{g.Name, "gateway", "—", "—", "—"})
	}
	p.table.SetRows(rows)
}

// serviceStatusCell renders the Status column, appending how the process
// ended for the exited/crashed states — "crashed (exit 1)" — so the two
// new supervision states stay distinguishable from stopped/failed at a
// glance.
func serviceStatusCell(s orchestrator.ServiceState) string {
	switch {
	case s.ExitCode != nil:
		return fmt.Sprintf("%s (exit %d)", s.Status, *s.ExitCode)
	case s.Signal != "":
		return fmt.Sprintf("%s (%s)", s.Status, s.Signal)
	default:
		return string(s.Status)
	}
}

// serviceByName finds name's current state in the panel's last poll.
func (p *servicesPanel) serviceByName(name string) (orchestrator.ServiceState, bool) {
	for _, s := range p.services {
		if s.Name == name {
			return s, true
		}
	}
	return orchestrator.ServiceState{}, false
}

// selected returns the currently highlighted service, if any.
func (p *servicesPanel) selected() (orchestrator.ServiceState, bool) {
	idx := p.table.Cursor()
	if idx < 0 || idx >= len(p.services) {
		return orchestrator.ServiceState{}, false
	}
	return p.services[idx], true
}

// logTailLines is how many lines the log view requests per fetch — more
// than any terminal shows at once, so scrolling context survives resize.
const logTailLines = 200

// fetchServiceLog fetches name's log tail — GET /api/services/{name}/logs.
func fetchServiceLog(client apiClient, name string) tea.Cmd {
	return func() tea.Msg {
		content, err := client.ServiceLogs(context.Background(), name, logTailLines)
		return serviceLogMsg{service: name, content: content, err: err}
	}
}

// logPollCmd re-fetches the open log view on the poll tick, so the tail
// follows the file the way the table follows /api/status. Nil (no fetch)
// when no log view is open.
func (p *servicesPanel) logPollCmd(client apiClient) tea.Cmd {
	if p.logService == "" {
		return nil
	}
	return fetchServiceLog(client, p.logService)
}

// applyLog lands a log fetch's result, unless the view was closed or
// switched to another service while the fetch was in flight.
func (p *servicesPanel) applyLog(msg serviceLogMsg) {
	if msg.service != p.logService {
		return
	}
	if msg.err != nil {
		p.status = msg.err.Error()
		return
	}
	p.logContent = msg.content
}

// restartCmd/flipCmd/seedCmd fire their action for name and report the
// outcome as an actionMsg; the panel's own table refreshes on the next
// poll tick rather than optimistically, since a restart/flip/seed's
// effect (status flipping through starting -> healthy) unfolds over time.
func restartCmd(client apiClient, name string) tea.Cmd {
	return func() tea.Msg {
		st, err := client.Restart(context.Background(), name)
		return actionMsg{action: "restart", service: name, state: st, err: err}
	}
}

func flipCmd(client apiClient, name string) tea.Cmd {
	return func() tea.Msg {
		st, err := client.Flip(context.Background(), name)
		return actionMsg{action: "flip", service: name, state: st, err: err}
	}
}

func seedCmd(client apiClient, name string) tea.Cmd {
	return func() tea.Msg {
		resp, err := client.Seed(context.Background(), name)
		state := orchestrator.ServiceState{Name: name}
		if err == nil && !resp.OK {
			err = fmt.Errorf("seed %s: %s", name, resp.Error)
		}
		return actionMsg{action: "seed", service: name, state: state, err: err}
	}
}

// update handles panel-local key bindings (restart/flip/seed on the
// selected row) and table navigation. Global keys (tab switch, quit) are
// handled by model before reaching here.
func (p *servicesPanel) update(msg tea.Msg, client apiClient) tea.Cmd {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		// The log view swallows every key except its own close/refresh —
		// table navigation underneath a full-screen log would silently
		// move the selection.
		if p.logService != "" {
			switch msg.String() {
			case "esc", "l":
				p.logService = ""
				p.logContent = ""
			}
			return nil
		}
		switch msg.String() {
		case "r":
			if svc, ok := p.selected(); ok {
				p.status = "restarting " + svc.Name + "…"
				return restartCmd(client, svc.Name)
			}
		case "v":
			if svc, ok := p.selected(); ok {
				p.status = "flipping " + svc.Name + "…"
				return flipCmd(client, svc.Name)
			}
		case "s":
			if svc, ok := p.selected(); ok {
				p.status = "seeding " + svc.Name + "…"
				return seedCmd(client, svc.Name)
			}
		case "l":
			if svc, ok := p.selected(); ok {
				p.logService = svc.Name
				p.logContent = ""
				return fetchServiceLog(client, svc.Name)
			}
		}
	}
	var cmd tea.Cmd
	p.table, cmd = p.table.Update(msg)
	return cmd
}

func (p *servicesPanel) applyAction(msg actionMsg) {
	if msg.err != nil {
		p.status = fmt.Sprintf("%s %s: %v", msg.action, msg.service, msg.err)
		return
	}
	p.status = fmt.Sprintf("%s %s: ok", msg.action, msg.service)
}

func (p *servicesPanel) view(width, height int) string {
	if p.logService != "" {
		return p.logView(height)
	}
	p.table.SetColumns(fitColumns(p.cols, width))
	p.table.SetWidth(width)
	p.table.SetHeight(height - 2)
	if p.loading {
		return "loading services…"
	}
	body := p.table.View()
	help := helpStyle.Render("↑/↓ move · r restart · v flip variant · s seed · l logs")
	if p.status != "" {
		help = helpStyle.Render(p.status) + "  " + help
	}
	return body + "\n" + help
}

// logView renders the open log tail in place of the table: a header naming
// the service, the last lines that fit, and the close hint. Content
// refreshes with the poll tick (see logPollCmd), so it follows the file
// without a dedicated stream.
func (p *servicesPanel) logView(height int) string {
	body := "(no log output yet)"
	if p.logContent != "" {
		lines := strings.Split(strings.TrimRight(p.logContent, "\n"), "\n")
		visible := max(height-3, 1)
		if len(lines) > visible {
			lines = lines[len(lines)-visible:]
		}
		body = strings.Join(lines, "\n")
	}
	header := helpStyle.Render("logs: " + p.logService)
	// Someone opening a crashed service's log is hunting for the reason —
	// keep the state (and its exit detail) in view while they read.
	if s, ok := p.serviceByName(p.logService); ok && (s.Status == orchestrator.StatusCrashed || s.Status == orchestrator.StatusExited) {
		cell := serviceStatusCell(s)
		if s.Status == orchestrator.StatusCrashed {
			cell = errorStyle.Render(cell)
		}
		header += " · " + cell
	}
	help := helpStyle.Render("esc close · refreshes with the status poll")
	return header + "\n" + body + "\n" + help
}
