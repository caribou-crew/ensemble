package tui

import (
	"strings"
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

func TestServicesPanelLogKeyOpensLogView(t *testing.T) {
	p := newServicesPanel()
	p.setServices([]orchestrator.ServiceState{{Name: "catalog", Status: orchestrator.StatusHealthy}})
	fc := &fakeAPIClient{logsContent: "hello-log\nline-2\n"}

	cmd := p.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")}, fc)
	if p.logService != "catalog" {
		t.Fatalf("logService = %q, want catalog", p.logService)
	}
	if cmd == nil {
		t.Fatal("expected a log fetch Cmd")
	}
	msg, ok := cmd().(serviceLogMsg)
	if !ok || msg.service != "catalog" {
		t.Fatalf("unexpected msg: %#v", msg)
	}
	if got := fc.Calls(); len(got) != 1 || got[0] != "logs:catalog" {
		t.Fatalf("expected client.ServiceLogs(catalog), got %v", got)
	}
	p.applyLog(msg)
	if view := p.view(80, 20); !strings.Contains(view, "hello-log") {
		t.Fatalf("log view should show the fetched tail, got:\n%s", view)
	}

	// esc closes the view and drops the buffered content.
	p.update(tea.KeyMsg{Type: tea.KeyEsc}, fc)
	if p.logService != "" || p.logContent != "" {
		t.Fatalf("esc should close the log view, got %q/%q", p.logService, p.logContent)
	}
}

// A log fetch resolving after the view was closed (or switched) must not
// resurrect stale content.
func TestServicesPanelStaleLogResultIsDropped(t *testing.T) {
	p := newServicesPanel()
	p.applyLog(serviceLogMsg{service: "catalog", content: "stale"})
	if p.logContent != "" {
		t.Fatalf("logContent = %q, want empty when no log view is open", p.logContent)
	}
}

func TestServicesPanelRendersExitStates(t *testing.T) {
	p := newServicesPanel()
	code := 1
	p.setServices([]orchestrator.ServiceState{
		{Name: "catalog", Status: orchestrator.StatusCrashed, ExitCode: &code},
		{Name: "worker", Status: orchestrator.StatusExited, ExitCode: new(int)},
	})
	rows := p.table.Rows()
	if rows[0][1] != "crashed (exit 1)" {
		t.Errorf("crashed row status = %q, want \"crashed (exit 1)\"", rows[0][1])
	}
	if rows[1][1] != "exited (exit 0)" {
		t.Errorf("exited row status = %q, want \"exited (exit 0)\"", rows[1][1])
	}
}
