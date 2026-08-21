package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/caribou-crew/ensemble/ensemble/orchestrator"
)

func TestServicesPanelPollUpdatesTable(t *testing.T) {
	p := newServicesPanel()
	p.applyStatus(statusMsg{resp: StatusResponse{Services: []orchestrator.ServiceState{
		{Name: "catalog", Status: orchestrator.StatusHealthy},
		{Name: "storefront", Status: orchestrator.StatusUnhealthy},
	}}})

	if len(p.services) != 2 {
		t.Fatalf("expected 2 services, got %d", len(p.services))
	}
	if p.loading {
		t.Fatal("expected loading to clear after a successful poll")
	}
	rows := p.table.Rows()
	if len(rows) != 2 || rows[0][0] != "catalog" {
		t.Fatalf("unexpected rows: %+v", rows)
	}
}

func TestServicesPanelPollError(t *testing.T) {
	p := newServicesPanel()
	p.applyStatus(statusMsg{err: errBoom})
	if p.status == "" {
		t.Fatal("expected the error to be recorded in status")
	}
}

func TestServicesPanelRestartKeyCallsRestart(t *testing.T) {
	p := newServicesPanel()
	p.setServices([]orchestrator.ServiceState{{Name: "catalog"}})
	fc := &fakeAPIClient{}

	cmd := p.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")}, fc)
	if cmd == nil {
		t.Fatal("expected a restart Cmd")
	}
	msg := cmd()
	action, ok := msg.(actionMsg)
	if !ok || action.action != "restart" || action.service != "catalog" {
		t.Fatalf("unexpected msg: %#v", msg)
	}
	if got := fc.Calls(); len(got) != 1 || got[0] != "restart:catalog" {
		t.Fatalf("expected client.Restart(catalog), got %v", got)
	}
}

func TestServicesPanelFlipKeyCallsFlip(t *testing.T) {
	p := newServicesPanel()
	p.setServices([]orchestrator.ServiceState{{Name: "catalog"}})
	fc := &fakeAPIClient{}

	cmd := p.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("v")}, fc)
	msg := cmd().(actionMsg)
	if msg.action != "flip" || msg.service != "catalog" {
		t.Fatalf("unexpected msg: %#v", msg)
	}
	if got := fc.Calls(); len(got) != 1 || got[0] != "flip:catalog" {
		t.Fatalf("expected client.Flip(catalog), got %v", got)
	}
}

func TestServicesPanelSeedKeyCallsSeed(t *testing.T) {
	p := newServicesPanel()
	p.setServices([]orchestrator.ServiceState{{Name: "catalog"}})
	fc := &fakeAPIClient{seedResult: SeedResponse{OK: true}}

	cmd := p.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")}, fc)
	msg := cmd().(actionMsg)
	if msg.action != "seed" || msg.err != nil {
		t.Fatalf("unexpected msg: %#v", msg)
	}
	if got := fc.Calls(); len(got) != 1 || got[0] != "seed:catalog" {
		t.Fatalf("expected client.Seed(catalog), got %v", got)
	}
}

func TestServicesPanelSeedFailureSurfacesError(t *testing.T) {
	p := newServicesPanel()
	p.setServices([]orchestrator.ServiceState{{Name: "catalog"}})
	fc := &fakeAPIClient{seedResult: SeedResponse{OK: false, Error: "boom"}}

	cmd := p.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")}, fc)
	msg := cmd().(actionMsg)
	if msg.err == nil {
		t.Fatal("expected a partial-failure seed to surface an error")
	}
}

var errBoom = &staticError{"boom"}

type staticError struct{ s string }

func (e *staticError) Error() string { return e.s }
