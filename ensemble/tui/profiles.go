package tui

import (
	"context"
	"strings"

	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/caribou-crew/ensemble/ensemble/orchestrator"
)

// profilesPanel is the Profiles tab: declared profiles and which are up
// (GET /api/profiles, polled), with up/down actions — matches the profile
// controls already exposed by the control-plane API.
type profilesPanel struct {
	table    table.Model
	cols     []table.Column // ideal (untruncated) column widths — see fitColumns
	profiles []orchestrator.ProfileInfo
	status   string
	loading  bool
}

func newProfilesPanel() profilesPanel {
	cols := []table.Column{
		{Title: "Profile", Width: 20},
		{Title: "Up", Width: 5},
		{Title: "Services", Width: 40},
	}
	t := table.New(table.WithColumns(cols), table.WithFocused(true), table.WithHeight(12))
	return profilesPanel{table: t, cols: cols, loading: true}
}

func fetchProfiles(client apiClient) tea.Cmd {
	return func() tea.Msg {
		resp, err := client.Profiles(context.Background())
		return profilesMsg{resp: resp, err: err}
	}
}

func profileUpCmd(client apiClient, name string) tea.Cmd {
	return func() tea.Msg {
		resp, err := client.ProfileUp(context.Background(), name)
		return profilesMsg{resp: resp, err: err}
	}
}

func profileDownCmd(client apiClient, name string) tea.Cmd {
	return func() tea.Msg {
		resp, err := client.ProfileDown(context.Background(), name)
		return profilesMsg{resp: resp, err: err}
	}
}

func (p *profilesPanel) applyProfiles(msg profilesMsg) {
	p.loading = false
	if msg.err != nil {
		p.status = msg.err.Error()
		return
	}
	p.setProfiles(msg.resp.Profiles)
}

func (p *profilesPanel) setProfiles(profiles []orchestrator.ProfileInfo) {
	p.profiles = profiles
	rows := make([]table.Row, len(profiles))
	for i, pr := range profiles {
		up := "no"
		if pr.Active {
			up = "yes"
		}
		rows[i] = table.Row{pr.Name, up, strings.Join(pr.Services, ", ")}
	}
	p.table.SetRows(rows)
}

func (p *profilesPanel) selected() (orchestrator.ProfileInfo, bool) {
	idx := p.table.Cursor()
	if idx < 0 || idx >= len(p.profiles) {
		return orchestrator.ProfileInfo{}, false
	}
	return p.profiles[idx], true
}

func (p *profilesPanel) update(msg tea.Msg, client apiClient) tea.Cmd {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "+":
			if pr, ok := p.selected(); ok {
				p.status = "bringing " + pr.Name + " up…"
				return profileUpCmd(client, pr.Name)
			}
		case "-":
			if pr, ok := p.selected(); ok {
				p.status = "bringing " + pr.Name + " down…"
				return profileDownCmd(client, pr.Name)
			}
		}
	}
	var cmd tea.Cmd
	p.table, cmd = p.table.Update(msg)
	return cmd
}

func (p *profilesPanel) view(width, height int) string {
	p.table.SetColumns(fitColumns(p.cols, width))
	p.table.SetWidth(width)
	p.table.SetHeight(height - 2)
	if p.loading {
		return "loading profiles…"
	}
	body := p.table.View()
	help := helpStyle.Render("↑/↓ move · + bring up · - bring down")
	if p.status != "" {
		help = helpStyle.Render(p.status) + "  " + help
	}
	return body + "\n" + help
}
