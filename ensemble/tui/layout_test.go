package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/table"
)

func TestFitColumnsNoOpWhenItFits(t *testing.T) {
	cols := []table.Column{{Title: "A", Width: 10}, {Title: "B", Width: 10}}
	got := fitColumns(cols, 100)
	if got[0].Width != 10 || got[1].Width != 10 {
		t.Fatalf("expected columns unchanged, got %+v", got)
	}
}

func TestFitColumnsScalesDownProportionally(t *testing.T) {
	cols := []table.Column{{Title: "A", Width: 20}, {Title: "B", Width: 20}}
	got := fitColumns(cols, 20)

	total := 0
	for _, c := range got {
		total += c.Width
	}
	if total > 20 {
		t.Fatalf("expected total width <= 20, got %d (%+v)", total, got)
	}
}

func TestFitColumnsDropsColumnsOnExtremelyNarrowWidth(t *testing.T) {
	cols := []table.Column{
		{Title: "Service", Width: 22},
		{Title: "Status", Width: 12},
		{Title: "Placement", Width: 10},
		{Title: "Variant", Width: 14},
		{Title: "Port", Width: 6},
	}
	got := fitColumns(cols, 15)

	// bubbles/table treats a Width<=0 column as hidden; the last (lowest
	// priority) columns should be the ones that disappear.
	if got[len(got)-1].Width > 0 && got[0].Width == 0 {
		t.Fatalf("expected the first (highest-priority) column to survive over the last, got %+v", got)
	}
	total := 0
	for _, c := range got {
		if c.Width > 0 {
			total += c.Width
		}
	}
	if total > 15 {
		t.Fatalf("expected surviving columns to sum to <= 15, got %d (%+v)", total, got)
	}
}

func TestTrafficPanelHidesDetailPaneWhenNarrow(t *testing.T) {
	p := newTrafficPanel()
	view := p.view(narrowTrafficWidth-1, 24)
	if strings.Contains(view, "no hop selected") {
		t.Fatalf("expected the detail pane to be hidden below narrowTrafficWidth, got:\n%s", view)
	}

	wide := p.view(100, 24)
	if !strings.Contains(wide, "no hop selected") {
		t.Fatalf("expected the detail pane to be shown at full width, got:\n%s", wide)
	}
}
