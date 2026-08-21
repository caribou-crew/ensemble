package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/caribou-crew/ensemble/ensemble/orchestrator"
)

func TestProfilesPanelPollUpdatesTable(t *testing.T) {
	p := newProfilesPanel()
	p.applyProfiles(profilesMsg{resp: orchestrator.ProfilesState{Profiles: []orchestrator.ProfileInfo{
		{Name: "lane1", Active: true, Services: []string{"catalog"}},
		{Name: "lane2", Active: false},
	}}})

	if len(p.profiles) != 2 {
		t.Fatalf("expected 2 profiles, got %d", len(p.profiles))
	}
	rows := p.table.Rows()
	if rows[0][1] != "yes" || rows[1][1] != "no" {
		t.Fatalf("unexpected rows: %+v", rows)
	}
}

func TestProfilesPanelUpKeyCallsProfileUp(t *testing.T) {
	p := newProfilesPanel()
	p.setProfiles([]orchestrator.ProfileInfo{{Name: "lane1"}})
	fc := &fakeAPIClient{}

	cmd := p.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("+")}, fc)
	if cmd == nil {
		t.Fatal("expected a profile-up Cmd")
	}
	cmd()
	if got := fc.Calls(); len(got) != 1 || got[0] != "profile-up:lane1" {
		t.Fatalf("expected client.ProfileUp(lane1), got %v", got)
	}
}

func TestProfilesPanelDownKeyCallsProfileDown(t *testing.T) {
	p := newProfilesPanel()
	p.setProfiles([]orchestrator.ProfileInfo{{Name: "lane1"}})
	fc := &fakeAPIClient{}

	cmd := p.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("-")}, fc)
	cmd()
	if got := fc.Calls(); len(got) != 1 || got[0] != "profile-down:lane1" {
		t.Fatalf("expected client.ProfileDown(lane1), got %v", got)
	}
}
