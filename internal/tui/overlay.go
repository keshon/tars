package tui

// Overlay stack (pi steal, minimal): one slot per open overlay, each
// remembering what had focus so teardown restores it. Gates are the
// first client (P10-4); the help overlay (P10-7) reuses the same
// push/pop instead of inventing its own focus dance.

// focusOwner names what receives keys when no overlay is open.
type focusOwner int

const (
	focusViewport focusOwner = iota
	focusInput
	focusNote
)

type overlayKind int

const (
	ovNone overlayKind = iota
	ovGate
	ovDialog
)

type overlayFrame struct {
	kind  overlayKind
	focus focusOwner
}

// pushOverlay records the current focus owner and opens an overlay.
// popOverlay closes the top overlay and restores its focus. An
// unbalanced pop is a no-op, so event-driven teardown (resolve,
// quit, cancel) can never underflow the stack.
func (m *model) pushOverlay(kind overlayKind) {
	focus := focusViewport
	switch {
	case m.note.Focused():
		focus = focusNote
	case m.input.Focused():
		focus = focusInput
	}
	m.overlays = append(m.overlays, overlayFrame{kind: kind, focus: focus})
}

func (m *model) popOverlay() {
	if len(m.overlays) == 0 {
		return
	}
	top := m.overlays[len(m.overlays)-1]
	m.overlays = m.overlays[:len(m.overlays)-1]
	m.applyFocus(top.focus)
}

func (m *model) applyFocus(f focusOwner) {
	m.input.Blur()
	m.note.Blur()
	switch f {
	case focusInput:
		m.input.Focus()
	case focusNote:
		m.note.Focus()
	}
}
