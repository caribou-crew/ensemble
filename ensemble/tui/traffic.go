package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/caribou-crew/ensemble/core/trace"
)

// trafficBufferCap bounds how many hops the panel keeps in memory — a long
// `ensemble up --tui` session shouldn't grow this unbounded. Oldest hops
// are dropped first, same trade-off the web dashboard's traffic view makes
// by only ever holding what's been fetched/streamed in the browser tab.
const trafficBufferCap = 1000

// trafficPanel is the Traffic tab: a live-scrolling feed of hops from
// GET /api/traffic/stream (SSE) with a detail pane for the selected hop
// and an errors-only filter — the terminal collapse of the web
// dashboard's separate Traffic and Inspector views (see design.md; a
// terminal has no room for a third, graph-shaped Topology view).
type trafficPanel struct {
	table      table.Model
	cols       []table.Column // ideal (untruncated) column widths — see fitColumns
	all        []trace.Hop    // full ring buffer, oldest first
	visible    []trace.Hop    // all, or all filtered to errors-only
	errorsOnly bool
	followTail bool
	connected  bool
}

func newTrafficPanel() trafficPanel {
	cols := []table.Column{
		{Title: "From", Width: 14},
		{Title: "To", Width: 14},
		{Title: "Method", Width: 7},
		{Title: "Path", Width: 30},
		{Title: "Status", Width: 6},
		{Title: "ms", Width: 8},
	}
	t := table.New(table.WithColumns(cols), table.WithFocused(true), table.WithHeight(10))
	return trafficPanel{table: t, cols: cols, followTail: true}
}

// waitForHop blocks for the next hop on ch and re-arms itself — the
// standard Bubble Tea pattern for consuming a Go channel: each Cmd
// delivers exactly one Msg, so the handler for hopMsg issues another
// waitForHop to keep listening.
func waitForHop(ch <-chan trace.Hop) tea.Cmd {
	return func() tea.Msg {
		hop, ok := <-ch
		if !ok {
			return hopStreamClosedMsg{}
		}
		return hopMsg{hop: hop}
	}
}

func isErrorHop(h trace.Hop) bool {
	return h.Err != "" || h.Status >= 400
}

func (p *trafficPanel) appendHop(h trace.Hop) {
	p.connected = true
	p.all = append(p.all, h)
	if len(p.all) > trafficBufferCap {
		p.all = p.all[len(p.all)-trafficBufferCap:]
	}
	if !p.errorsOnly || isErrorHop(h) {
		p.visible = append(p.visible, h)
		if len(p.visible) > trafficBufferCap {
			p.visible = p.visible[len(p.visible)-trafficBufferCap:]
		}
		p.refreshRows()
	}
}

func (p *trafficPanel) setErrorsOnly(v bool) {
	p.errorsOnly = v
	if v {
		p.visible = p.visible[:0]
		for _, h := range p.all {
			if isErrorHop(h) {
				p.visible = append(p.visible, h)
			}
		}
	} else {
		p.visible = append([]trace.Hop(nil), p.all...)
	}
	p.refreshRows()
}

func (p *trafficPanel) refreshRows() {
	rows := make([]table.Row, len(p.visible))
	for i, h := range p.visible {
		rows[i] = table.Row{h.From, h.To, h.Method, h.Path, statusCell(h), fmt.Sprintf("%.0f", hopDurationMs(h))}
	}
	cursor := p.table.Cursor()
	p.table.SetRows(rows)
	if p.followTail {
		p.table.GotoBottom()
	} else if cursor < len(rows) {
		p.table.SetCursor(cursor)
	}
}

func statusCell(h trace.Hop) string {
	if h.Err != "" {
		return "ERR"
	}
	if h.Status == 0 {
		return "-"
	}
	return fmt.Sprintf("%d", h.Status)
}

func hopDurationMs(h trace.Hop) float64 {
	if h.T.DoneMs > 0 {
		return h.T.DoneMs
	}
	return h.T.FirstByteMs
}

// selected returns the hop for the table's current cursor, if any.
func (p *trafficPanel) selected() (trace.Hop, bool) {
	idx := p.table.Cursor()
	if idx < 0 || idx >= len(p.visible) {
		return trace.Hop{}, false
	}
	return p.visible[idx], true
}

var scrollUpKeys = map[string]bool{
	"up": true, "k": true, "pgup": true, "b": true, "home": true, "g": true,
}

func (p *trafficPanel) update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch {
		case msg.String() == "e":
			p.setErrorsOnly(!p.errorsOnly)
			return nil
		case msg.String() == "G" || msg.String() == "end":
			p.followTail = true
			p.table.GotoBottom()
			return nil
		case scrollUpKeys[msg.String()]:
			p.followTail = false
		}
	}
	var cmd tea.Cmd
	p.table, cmd = p.table.Update(msg)
	return cmd
}

func (p *trafficPanel) view(width, height int) string {
	// design.md's narrow-terminal mitigation: drop the detail pane before
	// touching the table's own columns (those are handled by fitColumns
	// below regardless, for widths narrower than this).
	showDetail := width >= narrowTrafficWidth
	detailHeight := 0
	if showDetail {
		detailHeight = 6
	}
	listHeight := max(height-detailHeight-2, 3)

	p.table.SetColumns(fitColumns(p.cols, width))
	p.table.SetWidth(width)
	p.table.SetHeight(listHeight)

	var b strings.Builder
	b.WriteString(p.table.View())
	b.WriteString("\n")
	if showDetail {
		b.WriteString(p.detailView(width))
		b.WriteString("\n")
	}

	status := "connecting…"
	if p.connected {
		status = fmt.Sprintf("%d hops", len(p.all))
	}
	filter := ""
	if p.errorsOnly {
		filter = " · errors-only"
	}
	follow := ""
	if !p.followTail {
		follow = " · paused (G to resume)"
	}
	b.WriteString(helpStyle.Render(fmt.Sprintf("%s%s%s  ·  ↑/↓ select · e errors-only · G latest", status, filter, follow)))
	return b.String()
}

func (p *trafficPanel) detailView(width int) string {
	hop, ok := p.selected()
	if !ok {
		return helpStyle.Render("(no hop selected)")
	}
	line := fmt.Sprintf("%s %s -> %s  status=%s  %.0fms", hop.Method, hop.From, hop.To, statusCell(hop), hopDurationMs(hop))
	if hop.Err != "" {
		line += "  err=" + hop.Err
	}
	body := hop.Req.Body
	if body == "" {
		body = hop.Resp.Body
	}
	if len(body) > width*2 {
		body = body[:width*2] + "…"
	}
	if body == "" {
		return line
	}
	return line + "\n" + body
}
