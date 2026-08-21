package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/caribou-crew/ensemble/core/proxy"
)

func TestLatencyPanelPollUpdatesTable(t *testing.T) {
	p := newLatencyPanel()
	p.applyLatency(latencyMsg{resp: LatencyListResponse{Rules: []proxy.LatencyRule{
		{Target: "catalog", Path: "/", FixedMs: 100, Enabled: true},
	}}})

	if len(p.rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(p.rules))
	}
	rows := p.table.Rows()
	if rows[0][4] != "yes" {
		t.Fatalf("expected armed=yes, got row %+v", rows[0])
	}
}

func TestLatencyPanelArmAllKey(t *testing.T) {
	p := newLatencyPanel()
	fc := &fakeAPIClient{}

	cmd := p.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")}, fc)
	if cmd == nil {
		t.Fatal("expected an arm-all Cmd")
	}
	cmd()
	if got := fc.Calls(); len(got) != 1 || got[0] != "latency-arm-all" {
		t.Fatalf("expected client.LatencyArmAll(true), got %v", got)
	}
}

func TestLatencyPanelDisarmAllKey(t *testing.T) {
	p := newLatencyPanel()
	fc := &fakeAPIClient{}

	cmd := p.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("A")}, fc)
	cmd()
	if got := fc.Calls(); len(got) != 1 || got[0] != "latency-disarm-all" {
		t.Fatalf("expected client.LatencyArmAll(false), got %v", got)
	}
}

func TestLatencyPanelResetKey(t *testing.T) {
	p := newLatencyPanel()
	fc := &fakeAPIClient{}

	cmd := p.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")}, fc)
	cmd()
	if got := fc.Calls(); len(got) != 1 || got[0] != "latency-reset" {
		t.Fatalf("expected client.LatencyReset(), got %v", got)
	}
}
