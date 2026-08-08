//go:build windows

package main

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"

	"bindmount/internal/bindfilter"
)

// bindmount-gui is a minimal graphical helper that lists Bind Filter mappings
// in a message box. It is intentionally small: the CLI is the primary
// interface; this helper exists so a user can inspect global mappings without
// opening a terminal.

func main() {
	if err := runGUI(); err != nil {
		messageBox("bindmount-gui", err.Error(), MB_ICONERROR)
		os.Exit(1)
	}
}

func runGUI() error {
	mappings, err := bindfilter.ListVolume(`C:\`)
	if err != nil {
		return fmt.Errorf("query mappings: %w", err)
	}

	text := "No Bind Filter mappings found on C:\\.\r\n\r\nUse the bindmount CLI to create mappings.\r\n"
	if len(mappings) > 0 {
		text = fmt.Sprintf("Mappings on C:\\ (%d):\r\n\r\n", len(mappings))
		for _, m := range mappings {
			text += fmt.Sprintf("%s\r\n  flags: 0x%08X\r\n", m.VirtualRoot, m.Flags)
			for _, t := range m.Targets {
				text += fmt.Sprintf("  -> %s\r\n", t)
			}
			text += "\r\n"
		}
		text += "Use `bindmount remove <virtual-root>` (elevated) to remove a mapping.\r\n"
	}

	messageBox("bindmount-gui - Bind Filter mappings", text, MB_ICONINFORMATION)
	return nil
}

// ---------------------------------------------------------------------------
// Minimal user32 bindings.
// ---------------------------------------------------------------------------

const (
	MB_OK              = 0x00000000
	MB_ICONINFORMATION = 0x00000040
	MB_ICONERROR       = 0x00000010
)

var (
	user32          = syscall.NewLazyDLL("user32.dll")
	procMessageBoxW = user32.NewProc("MessageBoxW")
)

func messageBox(title, text string, style uint) {
	tPtr, _ := syscall.UTF16PtrFromString(title)
	xPtr, _ := syscall.UTF16PtrFromString(text)
	procMessageBoxW.Call(
		0,
		uintptr(unsafe.Pointer(tPtr)),
		uintptr(unsafe.Pointer(xPtr)),
		uintptr(style),
	)
}
