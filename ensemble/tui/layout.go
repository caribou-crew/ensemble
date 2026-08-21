package tui

import "github.com/charmbracelet/bubbles/table"

// minColWidth is the floor fitColumns scales a column down to before
// giving up on it entirely (see below) — small enough to still show a
// couple of characters plus bubbles/table's own "…" truncation marker.
const minColWidth = 4

// narrowTrafficWidth is the width below which the Traffic panel drops its
// detail pane rather than shrinking table columns further — design.md's
// risk note on narrow terminals: "drop the detail pane before dropping
// method/path/status."
const narrowTrafficWidth = 70

// fitColumns scales cols' widths down proportionally so they sum to at
// most width, when the panel's designed (ideal) widths don't already fit.
// Each panel keeps its ideal []table.Column and recomputes this on every
// render against the current terminal width — cheap for the handful of
// columns any panel has, and it means a resize is picked up immediately
// rather than only at construction.
//
// A column that would round below minColWidth is left at 0, which
// bubbles/table treats as "don't render this column" (see its renderRow) —
// on an extremely narrow terminal, the least-widthed (lowest priority,
// since panels list columns most-important-first) columns disappear
// instead of every column becoming an unreadable sliver.
func fitColumns(cols []table.Column, width int) []table.Column {
	total := 0
	for _, c := range cols {
		total += c.Width
	}
	if width <= 0 || total <= width {
		return cols
	}

	out := make([]table.Column, len(cols))
	remaining := width
	for i, c := range cols {
		if i == len(cols)-1 {
			out[i] = table.Column{Title: c.Title, Width: max(remaining, 0)}
			break
		}
		w := c.Width * width / total
		if w < minColWidth {
			w = 0
		}
		out[i] = table.Column{Title: c.Title, Width: w}
		remaining -= w
	}
	return out
}
