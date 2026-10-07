package tui

import "charm.land/lipgloss/v2"

const (
	colorDim                = "8"
	colorSuccess            = "2"
	colorError              = "1"
	colorAccent             = "6"
	colorWarning            = "3"
	colorUser               = "4"
	colorPopupText          = "#B6C2D2"
	colorPopupBackground    = "#1B2430"
	colorPopupBorder        = "#65768A"
	colorSelectedText       = "#F1F4F8"
	colorSelectedBackground = "#3B526F"
	colorDisabledText       = "#77818E"
	colorDisabledNumber     = "#20262E"
	colorDisabledLabel      = "#282E36"
	colorNumberText         = "#DCE5EF"
	colorNumberBackground   = "#303944"
	colorActionBackground   = "#526F91"
	colorMode               = "#65CCD0"
	colorReady              = "#A6C59A"
	colorBusy               = "#D7BC7C"
	colorFailed             = "#E58F93"
	colorSecondaryText      = "#8895A6"
	colorPopupError         = "#EF8790"
	colorSearchMatch        = "#F3D58A"
)

// Color choices and role styles live here. Renderers derive emphasis from them.
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
		dim:    lipgloss.NewStyle().Foreground(lipgloss.Color(colorDim)),
		add:    lipgloss.NewStyle().Foreground(lipgloss.Color(colorSuccess)),
		del:    lipgloss.NewStyle().Foreground(lipgloss.Color(colorError)),
		hunk:   lipgloss.NewStyle().Foreground(lipgloss.Color(colorAccent)),
		gate:   lipgloss.NewStyle().Foreground(lipgloss.Color(colorWarning)).Bold(true),
		status: lipgloss.NewStyle().Foreground(lipgloss.Color(colorDim)),
		user:   lipgloss.NewStyle().Foreground(lipgloss.Color(colorUser)).Bold(true),
		think:  lipgloss.NewStyle().Foreground(lipgloss.Color(colorDim)).Italic(true),
		err:    lipgloss.NewStyle().Foreground(lipgloss.Color(colorError)).Bold(true),
		warn:   lipgloss.NewStyle().Foreground(lipgloss.Color(colorWarning)),
	}
}

// popupStyles is shared by input completion and session search.
func popupStyles() (normal, border, selected lipgloss.Style) {
	normal = lipgloss.NewStyle().Foreground(lipgloss.Color(colorPopupText)).Background(lipgloss.Color(colorPopupBackground))
	border = normal.Foreground(lipgloss.Color(colorPopupBorder))
	selected = normal.Foreground(lipgloss.Color(colorSelectedText)).Background(lipgloss.Color(colorSelectedBackground)).Bold(true)
	return
}
