package tui

// Overlay frames remember the underlying pane for teardown. Message routing
// uses activeInputOwner rather than trusting a widget's cached focus flag.

// focusOwner identifies the zone that receives input.
type focusOwner int

const (
	focusViewport focusOwner = iota
	focusNavigator
	focusInput
	focusNote
	focusSearch
	focusHeader
	focusDialog
)

type overlayKind int

const (
	ovNone overlayKind = iota
	ovGate
	ovDialog
	ovSessions
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
	case m.navFocused:
		focus = focusNavigator
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
	m.navFocused = f == focusNavigator && m.sidebarVisible()
	switch f {
	case focusInput:
		m.input.Focus()
	case focusNote:
		m.note.Focus()
	case focusNavigator:
		if !m.navFocused {
			m.input.Focus()
		}
	}
}

// activeInputOwner is the single authority for keyboard, clipboard and caret routing.
func (m *model) activeInputOwner() focusOwner {
	if m.state == stPermission {
		if m.gstage == gsReject {
			return focusNote
		}
		return focusViewport
	}
	if m.state == stAsk {
		return focusInput
	}
	if m.dialog != nil {
		return focusDialog
	}
	if m.sessions != nil {
		switch m.sessions.mode {
		case sessRename:
			return focusNote
		case sessList:
			return focusSearch
		}
		return focusViewport
	}
	if m.header.focused {
		return focusHeader
	}
	if m.navFocused {
		return focusNavigator
	}
	return focusInput
}

func (m *model) syncFocus() {
	owner := m.activeInputOwner()
	if owner != focusInput {
		m.input.Blur()
	} else if !m.input.Focused() {
		m.input.Focus()
	}
	if owner != focusNote {
		m.note.Blur()
	} else if !m.note.Focused() {
		m.note.Focus()
	}
	if m.sessions != nil {
		if owner != focusSearch {
			m.sessions.filter.Blur()
		} else if !m.sessions.filter.Focused() {
			m.sessions.filter.Focus()
		}
	}
}
