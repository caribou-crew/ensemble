package tui

import (
	"context"
	"errors"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
)

// Run connects to the control plane at apiURL and takes over the terminal
// until the user quits or ctx is canceled (SIGINT/SIGTERM, or — when
// embedded in `ensemble up --tui` — the stack's own shutdown). It returns
// an error only for a genuine failure: the control plane unreachable at
// startup, or a terminal I/O error. A normal quit (user pressed q/ctrl+c)
// or ctx cancellation both return nil.
func Run(ctx context.Context, apiURL string) error {
	client := NewClient(apiURL)

	if _, err := client.Status(ctx); err != nil {
		return fmt.Errorf("%s is not reachable (is `ensemble up` running?): %w", apiURL, err)
	}

	p := tea.NewProgram(newModel(ctx, client), tea.WithContext(ctx), tea.WithAltScreen())
	_, err := p.Run()
	if err != nil && !errors.Is(err, tea.ErrProgramKilled) {
		return err
	}
	return nil
}
