package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/caribou-crew/ensemble/ensemble/orchestrator"
	"github.com/caribou-crew/ensemble/ensemble/server"
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

func TestServicesPanelTopologyAddsGatewayRows(t *testing.T) {
	p := newServicesPanel()
	p.applyStatus(statusMsg{resp: StatusResponse{Services: []orchestrator.ServiceState{
		{Name: "catalog", Status: orchestrator.StatusHealthy},
	}}})
	p.applyTopology(topologyMsg{resp: server.TopologyResponse{Nodes: []server.TopologyNode{
		{Name: "catalog", Category: "service", Status: "healthy"},
		{Name: "public", Category: "gateway", Status: "static", Entry: true},
		{Name: "payments-stub", Category: "stub", Status: "static"},
	}}})

	if len(p.gateways) != 1 || p.gateways[0].Name != "public" {
		t.Fatalf("expected exactly the gateway node, got %+v", p.gateways)
	}
	rows := p.table.Rows()
	if len(rows) != 2 || rows[0][0] != "catalog" || rows[1][0] != "public" {
		t.Fatalf("expected catalog then public as rows, got %+v", rows)
	}
	if rows[1][1] != "gateway" {
		t.Fatalf("expected the gateway row's status column to read \"gateway\", got %q", rows[1][1])
	}

	// A gateway row has no ServiceState, so it must never be selectable for
	// restart/flip/seed — cursor points past len(p.services).
	p.table.SetCursor(1)
	if _, ok := p.selected(); ok {
		t.Fatal("expected selected() to reject a gateway row")
	}
}

func TestServicesPanelTopologyErrorIsSilent(t *testing.T) {
	p := newServicesPanel()
	p.applyStatus(statusMsg{resp: StatusResponse{Services: []orchestrator.ServiceState{{Name: "catalog"}}}})
	p.applyTopology(topologyMsg{err: errBoom})

	if p.status != "" {
		t.Fatalf("expected a topology error not to overwrite panel status, got %q", p.status)
	}
	if len(p.table.Rows()) != 1 {
		t.Fatalf("expected the service row to survive a topology error, got %+v", p.table.Rows())
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
