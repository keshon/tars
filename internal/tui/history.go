package tui

// historyCap bounds the submitted-line history. Consecutive duplicates
// are not stored twice.
const historyCap = 50

// pushHistory records a submitted line.
func (m *model) pushHistory(line string) {
	if line == "" {
		return
	}
	if len(m.hist) > 0 && m.hist[len(m.hist)-1] == line {
		m.histIdx = len(m.hist)
		return
	}
	m.hist = append(m.hist, line)
	if len(m.hist) > historyCap {
		m.hist = m.hist[len(m.hist)-historyCap:]
	}
	m.histIdx = len(m.hist)
}

// historyWalk moves through history like a shell: up recalls older,
// down moves newer, and only at the caret edges — otherwise the keys
// move the cursor, so multiline edits are never lost to a recall.
// Returns true when it consumed the key.
func (m *model) historyWalk(up bool) bool {
	if up {
		if m.input.Line() != 0 || len(m.hist) == 0 {
			return false
		}
		if m.histIdx >= len(m.hist) {
			m.draft = m.input.Value()
		}
		if m.histIdx > 0 {
			m.histIdx--
		}
		m.input.SetValue(m.hist[m.histIdx])
		m.input.CursorEnd()
		return true
	}
	if m.input.Line() != m.input.LineCount()-1 || len(m.hist) == 0 {
		return false
	}
	if m.histIdx < len(m.hist) {
		m.histIdx++
	}
	if m.histIdx >= len(m.hist) {
		m.input.SetValue(m.draft)
	} else {
		m.input.SetValue(m.hist[m.histIdx])
	}
	m.input.CursorEnd()
	return true
}
