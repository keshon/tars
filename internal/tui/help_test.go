package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

func TestHelpPagesFitAndRemainReachable(t *testing.T) {
	for _, width := range []int{40, 80, 140} {
		for _, height := range []int{18, 30} {
			m := testModel()
			m.Update(tea.WindowSizeMsg{Width: width, Height: height})
			m.input.SetValue("draft\nsecond line")
			m.openHelp()
			for page, section := range helpSections() {
				if m.dialog.page != page {
					t.Fatalf("page = %d, want %d", m.dialog.page, page)
				}
				for _, offset := range []int{0, 1000} {
					m.dialog.offset = offset
					view := m.dialogView()
					if lipgloss.Height(view) > m.vp.Height() {
						t.Fatalf("%dx%d: help exceeds viewport height", width, height)
					}
					for _, line := range strings.Split(view, "\n") {
						if lipgloss.Width(line) > m.vp.Width() {
							t.Fatalf("%dx%d: help exceeds viewport width: %q", width, height, line)
						}
					}
					if offset > 0 {
						last := section.rows[len(section.rows)-1][1]
						words := strings.Fields(last)
						if !strings.Contains(view, words[len(words)-1]) {
							t.Fatalf("%dx%d: last action unreachable on %s", width, height, section.title+"\n"+view)
						}
					}
				}
				m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
				if m.dialog.offset != 0 {
					t.Fatal("category change must reset scrolling")
				}
			}
			if m.dialog.page != 0 {
				t.Fatal("categories must wrap")
			}
			m.Update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
			if m.dialog.page != len(helpSections())-1 {
				t.Fatal("Shift+Tab must go backwards")
			}
			m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
			if m.dialog != nil || m.input.Value() != "draft\nsecond line" {
				t.Fatal("help changed the draft")
			}
		}
	}
}
