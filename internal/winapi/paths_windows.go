//go:build windows

package winapi

import (
	"fmt"
	"strings"
	"syscall"
	"unsafe"
)

// ntVolumePrefixToDOS translates the \Device\HarddiskVolumeN prefix of an NT
// device path to its drive-letter form using QueryDosDevice, without opening
// any file. Unlike NTPathToDOS (GLOBALROOT open + GetFinalPathNameByHandle),
// this does not traverse Bind Filter mappings, so it is safe for virtual
// roots: opening a mapped path would resolve to the *target*, not the root.
//
// If the prefix is not recognized (not a local harddisk volume, or no drive
// letter exists), the input is returned unchanged with ok=false so callers
// can fall back to the raw NT form.
func ntVolumePrefixToDOS(ntPath string) (string, bool) {
	const devPrefix = `\Device\`
	if !strings.HasPrefix(ntPath, devPrefix) {
		return ntPath, false
	}
	rest := ntPath[len(devPrefix):]
	// Volume name runs up to the next backslash (or the whole string).
	vol := rest
	sep := strings.IndexByte(rest, '\\')
	if sep >= 0 {
		vol = rest[:sep]
	}
	if vol == "" {
		return ntPath, false
	}

	// Enumerate drive letters and compare each one's device name. There are
	// at most 26; this is cheaper and simpler than opening the volume.
	for letter := 'A'; letter <= 'Z'; letter++ {
		drive := string(letter) + ":"
		dev, err := queryDosDevice(drive)
		if err != nil {
			continue
		}
		if strings.EqualFold(dev, devPrefix+vol) {
			suffix := ""
			if sep >= 0 {
				suffix = rest[sep:]
			}
			return drive + suffix, true
		}
	}
	return ntPath, false
}

var (
	procQueryDosDeviceW           = modkernel32.NewProc("QueryDosDeviceW")
	procGetFinalPathNameByHandleW = modkernel32.NewProc("GetFinalPathNameByHandleW")
)

// queryDosDevice returns the NT device name a DOS device (e.g. "C:") maps to,
// e.g. "\Device\HarddiskVolume3".
func queryDosDevice(dosName string) (string, error) {
	namePtr, err := syscall.UTF16PtrFromString(dosName)
	if err != nil {
		return "", err
	}
	var buf [256]uint16
	n, _, callErr := procQueryDosDeviceW.Call(
		uintptr(unsafe.Pointer(namePtr)),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)),
	)
	if n == 0 {
		if callErr != syscall.Errno(0) {
			return "", callErr
		}
		return "", fmt.Errorf("QueryDosDevice(%q) failed", dosName)
	}
	// The buffer is a MULTI_SZ; the first string is the device name.
	units := buf[:n]
	end := 0
	for end < len(units) && units[end] != 0 {
		end++
	}
	return syscall.UTF16ToString(units[:end]), nil
}

// NTPathToDOS converts an NT device path of the form
// \Device\HarddiskVolumeN\... (as returned by BfGetMappings) to a Win32 path.
//
// It follows the go-winio approach: open the path through \\.\GLOBALROOT and
// ask the kernel for the final path name, then strip the \\?\ prefix.
//
// NOTE: this opens the path, so it traverses any Bind Filter mapping at that
// path. Use NTVirtualRootToDOS for mapping virtual roots.
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

// NTVirtualRootToDOS converts a virtual-root NT path to DOS form using
// prefix substitution only (no file open, no mapping traversal). Falls back
// to the raw NT form when the volume has no drive letter.
func NTVirtualRootToDOS(ntPath string) string {
	if dos, ok := ntVolumePrefixToDOS(ntPath); ok {
		return dos
	}
	return ntPath
}
