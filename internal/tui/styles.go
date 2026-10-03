package tui

import "github.com/charmbracelet/lipgloss"

// styles is the whole palette. All role styling lives here: no style
// literals elsewhere, so a future theme is one struct, not a hunt.
type styles struct {
	dim    lipgloss.Style
	add    lipgloss.Style
	del    lipgloss.Style
	hunk   lipgloss.Style
	gate   lipgloss.Style
	status lipgloss.Style
	user   lipgloss.Style
	think  lipgloss.Style
	err    lipgloss.Style
	warn   lipgloss.Style
}

func defaultStyles() styles {
	return styles{
		dim:    lipgloss.NewStyle().Foreground(lipgloss.Color("8")),
		add:    lipgloss.NewStyle().Foreground(lipgloss.Color("2")),
		del:    lipgloss.NewStyle().Foreground(lipgloss.Color("1")),
		hunk:   lipgloss.NewStyle().Foreground(lipgloss.Color("6")),
		gate:   lipgloss.NewStyle().Foreground(lipgloss.Color("3")).Bold(true),
		status: lipgloss.NewStyle().Foreground(lipgloss.Color("8")),
		user:   lipgloss.NewStyle().Foreground(lipgloss.Color("4")).Bold(true),
		think:  lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Italic(true),
		err:    lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Bold(true),
		warn:   lipgloss.NewStyle().Foreground(lipgloss.Color("3")),
	}
}
