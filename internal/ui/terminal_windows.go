package ui

import (
	"os"
	"syscall"
	"unsafe"
)

var (
	kernel32           = syscall.NewLazyDLL("kernel32.dll")
	procGetConsoleMode = kernel32.NewProc("GetConsoleMode")
	procSetConsoleMode = kernel32.NewProc("SetConsoleMode")
)

// enableVirtualTerminalProcessing is ENABLE_VIRTUAL_TERMINAL_PROCESSING: the
// output-mode bit that makes a Windows console interpret escape sequences
// instead of printing them.
const enableVirtualTerminalProcessing = 0x0004

// terminal asks the console itself rather than reading TERM, which Windows
// shells do not set. GetConsoleMode failing means f is not a console at all -
// a pipe, a file - which is the char-device test by another route.
// SetConsoleMode failing means a console too old to render escapes (before
// Windows 10 1511). Either way the answer is plain text, and nothing here can
// leave the console worse than it found it: the one bit it sets is the one
// that makes the bytes ptrbox is about to write mean what they say.
func terminal(f *os.File) bool {
	handle := f.Fd()
	var mode uint32
	if ok, _, _ := procGetConsoleMode.Call(handle, uintptr(unsafe.Pointer(&mode))); ok == 0 {
		return false
	}
	if mode&enableVirtualTerminalProcessing != 0 {
		return true
	}
	ok, _, _ := procSetConsoleMode.Call(handle, uintptr(mode|enableVirtualTerminalProcessing))
	return ok != 0
}
