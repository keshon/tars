//go:build windows

package tui

import (
	"context"
	"encoding/binary"
	"io"
	"os"
	"strings"
	"testing"
	"time"
	"unsafe"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	xwindows "github.com/charmbracelet/x/windows"
	"golang.org/x/sys/windows"
)

func nativeKeyRecord(vk, char uint16, modifiers uint32) xwindows.InputRecord {
	r := xwindows.InputRecord{EventType: xwindows.KEY_EVENT}
	binary.LittleEndian.PutUint32(r.Event[0:4], 1)
	binary.LittleEndian.PutUint16(r.Event[4:6], 1)
	binary.LittleEndian.PutUint16(r.Event[6:8], vk)
	binary.LittleEndian.PutUint16(r.Event[10:12], char)
	binary.LittleEndian.PutUint32(r.Event[12:16], modifiers)
	return r
}

func decodeConsoleEvents(t *testing.T, input string) []uv.Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	events := make(chan uv.Event, 64)
	reader := uv.NewTerminalReader(strings.NewReader(input), "")
	if err := reader.StreamEvents(ctx, events); err != nil {
		t.Fatal(err)
	}
	close(events)
	var result []uv.Event
	for event := range events {
		result = append(result, event)
	}
	return result
}

func TestNativeConsoleShiftEnterPreservesModifier(t *testing.T) {
	r := &consoleReader{}
	for _, record := range []xwindows.InputRecord{
		nativeKeyRecord('F', 'f', 0),
		nativeKeyRecord(xwindows.VK_RETURN, '\r', xwindows.SHIFT_PRESSED),
		nativeKeyRecord('S', 's', 0),
		nativeKeyRecord(xwindows.VK_RETURN, '\r', 0),
	} {
		r.encode(record)
	}
	events := decodeConsoleEvents(t, r.buffer.String())
	if len(events) != 4 {
		t.Fatalf("wrong event count: %#v", events)
	}
	m := sizeModel(t, testModel())
	m.state = stRunning
	for i, event := range events {
		key, ok := event.(uv.KeyPressEvent)
		if !ok {
			t.Fatalf("unexpected console event: %#v", event)
		}
		m.Update(tea.KeyPressMsg(key))
		if i == 2 && (m.input.Value() != "f\ns" || m.queued != "") {
			t.Fatal("native Shift+Enter submitted instead of inserting a newline")
		}
	}
	if m.queued != "f\ns" {
		t.Fatal("native plain Enter did not submit")
	}
}

func TestNativeConsoleDecodesPastedSurrogatePair(t *testing.T) {
	r := &consoleReader{}
	r.encode(nativeKeyRecord(0, 0xd83d, 0))
	r.encode(nativeKeyRecord(0, 0xde00, 0))
	events := decodeConsoleEvents(t, r.buffer.String())
	if len(events) != 1 {
		t.Fatalf("surrogate pair split into events: %#v", events)
	}
	key, ok := events[0].(uv.KeyPressEvent)
	if !ok || key.Text != "😀" {
		t.Fatalf("emoji corrupted: %#v", events)
	}
}

func TestConsoleReaderReadsRealNativeShiftEnter(t *testing.T) {
	handle := windows.Handle(os.Stdin.Fd())
	var original uint32
	if windows.GetConsoleMode(handle, &original) != nil {
		t.Skip("requires a console; run the compiled test binary in a terminal")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, restore, err := consoleInput(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer restore()
	var mode uint32
	if err := windows.GetConsoleMode(handle, &mode); err != nil || mode&windows.ENABLE_VIRTUAL_TERMINAL_INPUT != 0 {
		t.Fatal("native console mode was not selected")
	}
	record := nativeKeyRecord(xwindows.VK_RETURN, '\r', xwindows.SHIFT_PRESSED)
	var written uint32
	writeInput := windows.NewLazySystemDLL("kernel32.dll").NewProc("WriteConsoleInputW")
	wrote, _, callErr := writeInput.Call(uintptr(handle), uintptr(unsafe.Pointer(&record)), 1, uintptr(unsafe.Pointer(&written)))
	if wrote == 0 || written != 1 {
		t.Fatalf("write console record: %v", callErr)
	}
	r := &consoleReader{ctx: ctx, handle: handle}
	var buf [256]byte
	n, err := r.Read(buf[:])
	if err != nil {
		t.Fatal(err)
	}
	events := decodeConsoleEvents(t, string(buf[:n]))
	if len(events) != 1 {
		t.Fatalf("unexpected native events: %#v", events)
	}
	key, ok := events[0].(uv.KeyPressEvent)
	if !ok || tea.KeyPressMsg(key).String() != "shift+enter" {
		t.Fatalf("console lost Shift modifier: %#v", events)
	}
	if mode == original {
		t.Fatal("console input mode did not change")
	}
}

func TestConsoleReaderCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := &consoleReader{ctx: ctx}
	_, err := r.Read(make([]byte, 8))
	if err != io.EOF {
		t.Fatalf("canceled read returned %v", err)
	}
}
