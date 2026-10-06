//go:build windows

package tui

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	xwindows "github.com/charmbracelet/x/windows"
	"golang.org/x/sys/windows"
)

// Bubble Tea's default Windows VT reader drops console modifier flags.
// Keep native records and encode them using the Win32 protocol its decoder
// already understands. This also works in classic conhost, without host keymaps.
func consoleInput(ctx context.Context) ([]tea.ProgramOption, func(), error) {
	handle := windows.Handle(os.Stdin.Fd())
	var original uint32
	if err := windows.GetConsoleMode(handle, &original); err != nil {
		// Pipes and remote terminals continue through Bubble Tea's normal reader.
		return nil, func() {}, nil
	}
	mode := original &^ (windows.ENABLE_VIRTUAL_TERMINAL_INPUT | windows.ENABLE_LINE_INPUT |
		windows.ENABLE_ECHO_INPUT | windows.ENABLE_PROCESSED_INPUT | windows.ENABLE_QUICK_EDIT_MODE)
	mode |= windows.ENABLE_EXTENDED_FLAGS | windows.ENABLE_WINDOW_INPUT | windows.ENABLE_MOUSE_INPUT
	if err := windows.SetConsoleMode(handle, mode); err != nil {
		return nil, nil, fmt.Errorf("configure native console input: %w", err)
	}
	readCtx, cancel := context.WithCancel(ctx)
	r := &consoleReader{ctx: readCtx, handle: handle}
	cleanup := func() {
		cancel()
		_ = windows.SetConsoleMode(handle, original)
	}
	return []tea.ProgramOption{
		// Deliberately expose only io.Reader: Bubble Tea must not re-enable VT
		// input on this console or bypass the native record encoder.
		tea.WithInput(r),
		tea.WithFilter(func(_ tea.Model, msg tea.Msg) tea.Msg {
			if _, quitting := msg.(tea.QuitMsg); quitting {
				cancel()
			}
			return msg
		}),
	}, cleanup, nil
}

type consoleReader struct {
	ctx        context.Context
	handle     windows.Handle
	buffer     bytes.Buffer
	mouseState uint32
}

func (r *consoleReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for r.buffer.Len() == 0 {
		select {
		case <-r.ctx.Done():
			return 0, io.EOF
		default:
		}
		var records [64]xwindows.InputRecord
		var count uint32
		if err := xwindows.PeekConsoleInput(r.handle, &records[0], uint32(len(records)), &count); err != nil {
			return 0, err
		}
		if count == 0 {
			select {
			case <-r.ctx.Done():
				return 0, io.EOF
			case <-poll.C:
			}
			continue
		}
		if err := xwindows.ReadConsoleInput(r.handle, &records[0], count, &count); err != nil {
			return 0, err
		}
		for _, record := range records[:count] {
			r.encode(record)
		}
	}
	return r.buffer.Read(p)
}

func (r *consoleReader) encode(record xwindows.InputRecord) {
	switch record.EventType {
	case xwindows.KEY_EVENT:
		e := record.KeyEvent()
		down := 0
		if e.KeyDown {
			down = 1
		}
		// Encoding UTF-16 units as records lets the existing decoder join
		// surrogate pairs, including pasted emoji and terminal escape packets.
		fmt.Fprintf(&r.buffer, "\x1b[%d;%d;%d;%d;%d;%d_", e.VirtualKeyCode,
			e.VirtualScanCode, e.Char, down, e.ControlKeyState, e.RepeatCount)
	case xwindows.WINDOW_BUFFER_SIZE_EVENT:
		e := record.WindowBufferSizeEvent()
		r.buffer.WriteString(ansi.WindowOp(8, int(e.Size.Y), int(e.Size.X)))
	case xwindows.MOUSE_EVENT:
		e := record.MouseEvent()
		button, release := tea.MouseNone, false
		motion := e.EventFlags == xwindows.MOUSE_MOVED
		if e.EventFlags == xwindows.MOUSE_WHEELED || e.EventFlags == xwindows.MOUSE_HWHEELED {
			positive := int16(e.ButtonState>>16) > 0
			button = tea.MouseWheelDown
			if positive {
				button = tea.MouseWheelUp
			}
			if e.EventFlags == xwindows.MOUSE_HWHEELED {
				button = tea.MouseWheelLeft
				if positive {
					button = tea.MouseWheelRight
				}
			}
		} else {
			changed := (r.mouseState ^ e.ButtonState) & 0xffff
			release = changed != 0 && changed&e.ButtonState == 0
			buttons := changed
			if motion || changed == 0 {
				buttons = e.ButtonState
			}
			switch {
			case buttons&xwindows.FROM_LEFT_1ST_BUTTON_PRESSED != 0:
				button = tea.MouseLeft
			case buttons&xwindows.RIGHTMOST_BUTTON_PRESSED != 0:
				button = tea.MouseRight
			case buttons&xwindows.FROM_LEFT_2ND_BUTTON_PRESSED != 0:
				button = tea.MouseMiddle
			}
		}
		r.mouseState = e.ButtonState & 0xffff
		if motion && button == tea.MouseNone {
			return
		}
		flags := e.ControlKeyState
		r.buffer.WriteString(ansi.MouseSgr(ansi.EncodeMouseButton(button, motion,
			flags&xwindows.SHIFT_PRESSED != 0,
			flags&(xwindows.LEFT_ALT_PRESSED|xwindows.RIGHT_ALT_PRESSED) != 0,
			flags&(xwindows.LEFT_CTRL_PRESSED|xwindows.RIGHT_CTRL_PRESSED) != 0),
			int(e.MousePositon.X), int(e.MousePositon.Y), release))
	}
}
