package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestModifiedEnterAddsNewlineWithoutSubmitting(t *testing.T) {
	for _, state := range []runState{stDone, stAsk, stRunning} {
		for _, key := range []tea.KeyPressMsg{
			{Code: tea.KeyEnter, Mod: tea.ModShift},
			{Code: 'o', Mod: tea.ModCtrl},
		} {
			m := sizeModel(t, testModel())
			m.state = state
			for _, r := range "first" {
				m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
			}
			m.Update(key)
			m.Update(tea.KeyPressMsg{Code: 's', Text: "second"})
			if m.input.Value() != "first\nsecond" || m.input.Height() != 2 || m.state != state || m.queued != "" {
				t.Fatalf("%s in state %d submitted or lost multiline input: %q", key.String(), state, m.input.Value())
			}
			if cursor := m.View().Cursor; cursor == nil || cursor.Y != m.termH-m.footerRows()-1 {
				t.Fatalf("newline scrolled out earlier rows despite room in the composer: %+v", cursor)
			}
			if state == stRunning {
				m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
				if m.queued != "first\nsecond" || m.input.Value() != "" {
					t.Fatal("plain Enter did not queue the multiline follow-up")
				}
			}
		}
	}
}

func TestPasteGrowsComposerWithoutSubmitting(t *testing.T) {
	m := sizeModel(t, testModel())
	m.state = stDone
	m.Update(tea.PasteMsg{Content: "first\nsecond"})
	if m.input.Value() != "first\nsecond" || m.input.Height() != 2 || m.state != stDone {
		t.Fatal("paste submitted text or did not fit the composer")
	}
}

func TestWrappedInputGrowsComposer(t *testing.T) {
	m := testModel()
	m.state = stDone
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 24})
	m.Update(tea.PasteMsg{Content: "This is a longer paragraph that should wrap across multiple visible input rows."})
	if m.input.Height() < 2 || m.View().Cursor == nil {
		t.Fatal("wrapped text remained squeezed into a single row")
	}
}
