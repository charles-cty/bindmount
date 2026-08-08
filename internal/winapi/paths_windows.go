//go:build windows

package winapi

import (
	"fmt"
	"strings"
	"syscall"
	"unsafe"
)

var (
	procGetFinalPathNameByHandleW = modkernel32.NewProc("GetFinalPathNameByHandleW")
)

// NTPathToDOS converts an NT device path of the form
// \Device\HarddiskVolumeN\... (as returned by BfGetMappings) to a Win32 path.
//
// It follows the go-winio approach: open the path through \\.\GLOBALROOT and
// ask the kernel for the final path name, then strip the \\?\ prefix.
func NTPathToDOS(ntPath string) (string, error) {
	globalRoot := `\\.\GLOBALROOT` + ntPath
	p16, err := syscall.UTF16PtrFromString(globalRoot)
	if err != nil {
		return "", err
	}

	// FILE_FLAG_BACKUP_SEMANTICS lets us open directories as well as files.
	h, err := syscall.CreateFile(
		p16,
		0, // no data access needed; query only
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil,
		syscall.OPEN_EXISTING,
		syscall.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if err != nil {
		return "", fmt.Errorf("open %q via GLOBALROOT: %w", ntPath, err)
	}
	defer syscall.CloseHandle(h)

	var buf [syscall.MAX_LONG_PATH]uint16
	n, _, callErr := procGetFinalPathNameByHandleW.Call(
		uintptr(h),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)),
		0, // FILE_NAME_NORMALIZED, VOLUME_NAME_DOS
	)
	if n == 0 {
		if callErr != syscall.Errno(0) {
			return "", fmt.Errorf("GetFinalPathNameByHandle on %q: %w", ntPath, callErr)
		}
		return "", fmt.Errorf("GetFinalPathNameByHandle on %q failed", ntPath)
	}
	if int(n) >= len(buf) {
		return "", fmt.Errorf("final path name for %q exceeds %d chars", ntPath, len(buf))
	}

	path := syscall.UTF16ToString(buf[:n])
	// Strip the \\?\ or \\?\UNC\ decoration.
	path = strings.TrimPrefix(path, `\\?\UNC\`)
	if strings.HasPrefix(path, `\\?\`) {
		path = path[len(`\\?\`):]
	} else if strings.HasPrefix(path, `UNC\`) && !strings.HasPrefix(globalRoot, `\\?\UNC`) {
		// UNC\server\share -> \\server\share
		path = `\\` + path[len(`UNC\`):]
	}
	return path, nil
}
