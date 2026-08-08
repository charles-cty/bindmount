//go:build windows

package winapi

import (
	"errors"
	"fmt"
	"syscall"
)

// ErrBindfltUnavailable is returned when bindfltapi.dll or a required export
// cannot be loaded from %SystemRoot%\System32.
var ErrBindfltUnavailable = errors.New("bindfltapi.dll is not available on this system")

// hresultError converts a non-success HRESULT to a Go error.
func hresultError(hr uintptr) error {
	// syscall.Errno prints the Win32 message for facility-Win32 codes, which
	// covers the common E_INVALIDARG/E_ACCESSDENIED/E_PATH_NOT_FOUND cases
	// observed from bindfltapi.
	errno := syscall.Errno(hr & 0xFFFF)
	if hr&0xFFFF0000 == 0x80070000 {
		return errno
	}
	return fmt.Errorf("HRESULT 0x%08X", uint32(hr))
}

var modkernel32 = syscall.NewLazyDLL("kernel32.dll")
