package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func newTestModel(fc *fakeAPIClient) model {
	// Canceled before newModel starts StreamTraffic's background goroutine
	// (not after): that goroutine checks ctx on its very first loop
	// iteration before touching the client or the package's shared
	// reconnect-delay var, so an already-Done ctx guarantees it exits
	// immediately instead of leaking past this test — see stream_test.go's
	// setStreamReconnectDelayForTest for why a leaked goroutine matters.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m := newModel(ctx, fc)
	m.width, m.height = 100, 30
	return m
}

func TestModelStartsOnServicesTab(t *testing.T) {
	m := newTestModel(&fakeAPIClient{})
	if m.active != tabServices {
		t.Fatalf("expected default tab to be Services, got %v", m.active)
	}
}

func TestModelTabCyclesForwardAndBack(t *testing.T) {
	m := newTestModel(&fakeAPIClient{})

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = next.(model)
	if m.active != tabTraffic {
		t.Fatalf("expected Traffic after tab, got %v", m.active)
	}

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	m = next.(model)
	if m.active != tabServices {
		t.Fatalf("expected Services after shift+tab, got %v", m.active)
	}
}

func TestModelNumberKeysJumpToTab(t *testing.T) {
	m := newTestModel(&fakeAPIClient{})
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")})
	m = next.(model)
	if m.active != tabLatency {
		t.Fatalf("expected Latency after pressing 3, got %v", m.active)
	}
}

func TestModelSwitchingTabRefetchesActivePanel(t *testing.T) {
	fc := &fakeAPIClient{}
	m := newTestModel(fc)

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")})
	m = next.(model)
	if cmd == nil {
		t.Fatal("expected switching to Latency to issue a fetch Cmd")
	}
	msg := cmd()
	if _, ok := msg.(latencyMsg); !ok {
		t.Fatalf("expected a latencyMsg from the switch-tab fetch, got %#v", msg)
	}
}

func TestModelQuitOnQ(t *testing.T) {
	m := newTestModel(&fakeAPIClient{})
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if cmd == nil {
		t.Fatal("expected q to issue a Cmd")
	}
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Fatalf("expected tea.Quit, got %#v", msg)
	}
}

func TestModelTickRefreshesActivePanelOnly(t *testing.T) {
	orig := pollInterval
	pollInterval = time.Millisecond
	t.Cleanup(func() { pollInterval = orig })

	fc := &fakeAPIClient{}
	m := newTestModel(fc)
	m.active = tabProfiles

	_, cmd := m.Update(tickMsg{})
	if cmd == nil {
		t.Fatal("expected tick to issue a batched Cmd")
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		found := false
		for _, c := range batch {
			if pm, ok := c().(profilesMsg); ok {
				found = true
				_ = pm
			}
		}
		if !found {
			t.Fatal("expected the tick's batch to include a profiles fetch")
		}
		return
	}
	t.Fatalf("expected a tea.BatchMsg, got %#v", msg)
}

func TestModelViewShowsTabsAndGlobalFooter(t *testing.T) {
	m := newTestModel(&fakeAPIClient{})
	view := m.View()

	for _, want := range []string{"Services", "Traffic", "Latency", "Profiles", "quit"} {
		if !strings.Contains(view, want) {
			t.Fatalf("expected view to contain %q, got:\n%s", want, view)
		}
	}
}

func TestModelRoutesMessagesToPanels(t *testing.T) {
	m := newTestModel(&fakeAPIClient{})

	next, _ := m.Update(statusMsg{resp: StatusResponse{}})
	m = next.(model)
	if m.services.loading {
		t.Fatal("expected statusMsg to clear the Services panel's loading flag")
	}
}
