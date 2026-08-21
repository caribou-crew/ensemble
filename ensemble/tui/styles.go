package tui

import "github.com/charmbracelet/lipgloss"

var (
	tabBarStyle = lipgloss.NewStyle().
			BorderStyle(lipgloss.NormalBorder()).
			BorderBottom(true).
			BorderForeground(lipgloss.Color("240"))

	activeTabStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("212")).
			Padding(0, 2)

	inactiveTabStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("245")).
				Padding(0, 2)

	helpStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))

	errorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
)
