//go:build windows

package app

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32             = windows.NewLazySystemDLL("kernel32.dll")
	procPeekConsoleInput = kernel32.NewProc("PeekConsoleInputW")
	procReadConsoleInput = kernel32.NewProc("ReadConsoleInputW")
	procPeekNamedPipe    = kernel32.NewProc("PeekNamedPipe")
)

type inputRecord struct {
	EventType uint16
	_         uint16
	Event     [16]byte
}

const (
	recordKeyEvent   = 0x0001
	recordMouseEvent = 0x0002
)

func waitForConsoleInput(handle windows.Handle, timeout time.Duration, paused *atomic.Bool) error {
	deadline := time.Now().Add(timeout)
	for {
		if paused != nil && paused.Load() {
			return errInputPaused
		}

		var record inputRecord
		var numRead uint32
		r1, _, err := procPeekConsoleInput.Call(
			uintptr(handle),
			uintptr(unsafe.Pointer(&record)),
			1,
			uintptr(unsafe.Pointer(&numRead)),
		)
		if r1 == 0 {
			return err
		}

		if numRead == 0 {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				return os.ErrDeadlineExceeded
			}
			millis := uint32(remaining.Milliseconds())
			if millis < 1 {
				millis = 1
			}
			waitRes, waitErr := windows.WaitForSingleObject(handle, millis)
			if paused != nil && paused.Load() {
				return errInputPaused
			}
			if waitRes == uint32(windows.WAIT_TIMEOUT) {
				return os.ErrDeadlineExceeded
			}
			if waitRes != windows.WAIT_OBJECT_0 {
				if waitErr != nil {
					return waitErr
				}
				return errors.New("WaitForSingleObject failed")
			}
			continue
		}

		switch record.EventType {
		case recordKeyEvent:
			bKeyDown := binary.LittleEndian.Uint32(record.Event[0:4]) != 0
			if bKeyDown {
				return nil
			}
			// Key-up event: drain from buffer so ReadFile will not block
			var drain inputRecord
			var numDrain uint32
			procReadConsoleInput.Call(
				uintptr(handle),
				uintptr(unsafe.Pointer(&drain)),
				1,
				uintptr(unsafe.Pointer(&numDrain)),
			)
		case recordMouseEvent:
			return nil
		default:
			// Non-input event (focus, menu, buffer resize): drain it
			var drain inputRecord
			var numDrain uint32
			procReadConsoleInput.Call(
				uintptr(handle),
				uintptr(unsafe.Pointer(&drain)),
				1,
				uintptr(unsafe.Pointer(&numDrain)),
			)
		}

		if time.Now().After(deadline) {
			return os.ErrDeadlineExceeded
		}
	}
}

func waitForPipeInput(handle windows.Handle, timeout time.Duration, paused *atomic.Bool) error {
	deadline := time.Now().Add(timeout)
	for {
		if paused != nil && paused.Load() {
			return errInputPaused
		}
		var avail uint32
		r1, _, err := procPeekNamedPipe.Call(
			uintptr(handle),
			0,
			0,
			0,
			uintptr(unsafe.Pointer(&avail)),
			0,
		)
		if r1 == 0 {
			return err
		}
		if avail > 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return os.ErrDeadlineExceeded
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// readInputByteWithTimeout implements responsive input polling on Windows.
// For console handles, PeekConsoleInput and WaitForSingleObject prevent ReadFile
// from blocking on discarded key-up events or bare Escape probe timeouts.
// For pipes and files, PeekNamedPipe inspects available bytes before reading.
func readInputByteWithTimeout(reader io.Reader, timeout time.Duration, paused *atomic.Bool) (byte, error) {
	if paused != nil && paused.Load() {
		return 0, errInputPaused
	}
	file, ok := reader.(*os.File)
	if !ok {
		return readInputByte(reader)
	}

	handle := windows.Handle(file.Fd())
	var mode uint32
	if windows.GetConsoleMode(handle, &mode) == nil {
		if err := waitForConsoleInput(handle, timeout, paused); err != nil {
			return 0, err
		}
	} else {
		if err := waitForPipeInput(handle, timeout, paused); err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) || errors.Is(err, errInputPaused) {
				return 0, err
			}
		}
	}

	if paused != nil && paused.Load() {
		return 0, errInputPaused
	}
	return readInputByte(reader)
}
