package tui

import (
	"context"
	"fmt"
	"sort"

	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/caribou-crew/ensemble/core/proxy"
)

// latencyPanel is the Latency tab: the configured injection rules (GET
// /api/latency, polled), with arm-all and reset actions — terminal analog
// of the web dashboard's Latency view's rule table.
type latencyPanel struct {
	table   table.Model
	cols    []table.Column // ideal (untruncated) column widths — see fitColumns
	rules   []proxy.LatencyRule
	status  string
	loading bool
}

func newLatencyPanel() latencyPanel {
	cols := []table.Column{
		{Title: "Target", Width: 20},
		{Title: "Path", Width: 20},
		{Title: "Fixed ms", Width: 10},
		{Title: "p50/p95/p99", Width: 16},
		{Title: "Armed", Width: 7},
	}
	t := table.New(table.WithColumns(cols), table.WithFocused(true), table.WithHeight(12))
	return latencyPanel{table: t, cols: cols, loading: true}
}

func fetchLatency(client apiClient) tea.Cmd {
	return func() tea.Msg {
		resp, err := client.LatencyList(context.Background())
		return latencyMsg{resp: resp, err: err}
	}
}

func latencyArmAllCmd(client apiClient, enabled bool) tea.Cmd {
	return func() tea.Msg {
		resp, err := client.LatencyArmAll(context.Background(), enabled)
		return latencyMsg{resp: resp, err: err}
	}
}

func latencyResetCmd(client apiClient) tea.Cmd {
	return func() tea.Msg {
		resp, err := client.LatencyReset(context.Background())
		return latencyMsg{resp: resp, err: err}
	}
}

func (p *latencyPanel) applyLatency(msg latencyMsg) {
	p.loading = false
	if msg.err != nil {
		p.status = msg.err.Error()
		return
	}
	p.setRules(msg.resp.Rules)
}

func (p *latencyPanel) setRules(rules []proxy.LatencyRule) {
	sorted := append([]proxy.LatencyRule(nil), rules...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Target != sorted[j].Target {
			return sorted[i].Target < sorted[j].Target
		}
		return sorted[i].Path < sorted[j].Path
	})
	p.rules = sorted

	rows := make([]table.Row, len(sorted))
	for i, r := range sorted {
		armed := "no"
		if r.Enabled {
			armed = "yes"
		}
		percentiles := fmt.Sprintf("%.0f/%.0f/%.0f", r.P50, r.P95, r.P99)
		rows[i] = table.Row{r.Target, r.Path, fmt.Sprintf("%.0f", r.FixedMs), percentiles, armed}
	}
	p.table.SetRows(rows)
}

func (p *latencyPanel) update(msg tea.Msg, client apiClient) tea.Cmd {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "a":
			p.status = "arming all rules…"
			return latencyArmAllCmd(client, true)
		case "A":
			p.status = "disarming all rules…"
			return latencyArmAllCmd(client, false)
		case "x":
			p.status = "resetting rules…"
			return latencyResetCmd(client)
		}
	}
	var cmd tea.Cmd
	p.table, cmd = p.table.Update(msg)
	return cmd
}

func (p *latencyPanel) view(width, height int) string {
	p.table.SetColumns(fitColumns(p.cols, width))
	p.table.SetWidth(width)
	p.table.SetHeight(height - 2)
	if p.loading {
		return "loading latency rules…"
	}
	body := p.table.View()
	help := helpStyle.Render("↑/↓ move · a arm-all · A disarm-all · x reset")
	if p.status != "" {
		help = helpStyle.Render(p.status) + "  " + help
	}
	return body + "\n" + help
}
