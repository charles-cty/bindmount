//go:build windows

package winapi

import (
	"encoding/binary"
	"fmt"
	"syscall"
	"unicode/utf16"
)

const (
	// fileOpenReparsePoint instructs CreateFile not to follow reparse points,
	// so we get a handle to the reparse point node itself.
	fileOpenReparsePoint = 0x00200000

	// fsctlGetReparsePoint is the IOCTL code for FSCTL_GET_REPARSE_POINT.
	fsctlGetReparsePoint = 0x000900A8

	// IO_REPARSE_TAG_APPEXECLINK is the reparse tag used by Windows App
	// Execution Aliases (the zero-byte .exe stubs in
	// %LOCALAPPDATA%\Microsoft\WindowsApps\).
	ioReparseTagAppExecLink = 0x8000001B

	// maximumReparseDataBufferSize is the kernel-defined cap for reparse data
	// (16 KiB payload + 8-byte REPARSE_DATA_BUFFER header).
	maximumReparseDataBufferSize = 16384 + 8
)

// AppExecLinkReparseBuffer is the fixed-size prefix that precedes the
// variable-length string list in an APPEXECLINK reparse buffer.
//
// Wire layout (little-endian, offsets relative to start of REPARSE_DATA_BUFFER):
//
//	[0:4]   ULONG  ReparseTag        = 0x8000001B
//	[4:6]   USHORT ReparseDataLength
//	[6:8]   USHORT Reserved
//	[8:12]  ULONG  Version           (always 3 in practice)
//	[12:16] ULONG  StringCount       (typically 4)
//	[16:…]  null-terminated UTF-16LE strings, concatenated:
//	          [0] Package full name
//	          [1] Entry point (AppUserModelId)
//	          [2] Executable path        ← the value we want
//	          [3] Application type flag  ("0" for regular UWP)
const (
	appExecLinkHeaderSize    = 8  // ReparseTag + ReparseDataLength + Reserved
	appExecLinkVersionOffset = 8  // Version field
	appExecLinkCountOffset   = 12 // StringCount field
	appExecLinkStringsOffset = 16 // start of the string payload
	appExecLinkExeStringIdx  = 2  // index of the executable path in the list
)

// AppExecLinkInfo holds the decoded payload of an APPEXECLINK reparse point.
type AppExecLinkInfo struct {
	PackageFullName string // string[0]: e.g. "Microsoft.DesktopAppInstaller_1.29.280.0_x64__8wekyb3d8bbwe"
	ExePath         string // string[2]: real executable path
}

// ReadAppExecLinkInfo reads the full APPEXECLINK reparse data from path and
// returns the package full name (string[0]) and real executable path (string[2]).
func ReadAppExecLinkInfo(path string) (*AppExecLinkInfo, error) {
	p16, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := syscall.CreateFile(
		p16,
		0,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil,
		syscall.OPEN_EXISTING,
		syscall.FILE_FLAG_BACKUP_SEMANTICS|fileOpenReparsePoint,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("open reparse point %q: %w", path, err)
	}
	defer syscall.CloseHandle(h)

	var buf [maximumReparseDataBufferSize]byte
	var returned uint32
	err = syscall.DeviceIoControl(
		h,
		fsctlGetReparsePoint,
		nil, 0,
		&buf[0], uint32(len(buf)),
		&returned, nil,
	)
	if err != nil {
		return nil, fmt.Errorf("FSCTL_GET_REPARSE_POINT on %q: %w", path, err)
	}
	if returned < appExecLinkStringsOffset {
		return nil, fmt.Errorf("%q: reparse buffer too small (%d bytes)", path, returned)
	}
	tag := binary.LittleEndian.Uint32(buf[0:4])
	if tag != ioReparseTagAppExecLink {
		return nil, fmt.Errorf("%q: reparse tag 0x%08X is not APPEXECLINK", path, tag)
	}
	count := binary.LittleEndian.Uint32(buf[appExecLinkCountOffset : appExecLinkCountOffset+4])
	if count <= appExecLinkExeStringIdx {
		return nil, fmt.Errorf("%q: APPEXECLINK has only %d strings, need at least %d",
			path, count, appExecLinkExeStringIdx+1)
	}

	strings, err := parseAllAppExecLinkStrings(buf[appExecLinkStringsOffset:returned], count)
	if err != nil {
		return nil, err
	}
	return &AppExecLinkInfo{
		PackageFullName: strings[0],
		ExePath:         strings[appExecLinkExeStringIdx],
	}, nil
}

// ReadAppExecLink is a convenience wrapper that returns only the real
// executable path (string[2]) from an APPEXECLINK reparse point.
func ReadAppExecLink(path string) (string, error) {
	info, err := ReadAppExecLinkInfo(path)
	if err != nil {
		return "", err
	}
	return info.ExePath, nil
}

// parseAllAppExecLinkStrings decodes all null-terminated UTF-16LE strings
// from the APPEXECLINK string payload and returns them as a slice.
func parseAllAppExecLinkStrings(data []byte, count uint32) ([]string, error) {
	out := make([]string, 0, count)
	pos := 0
	for i := uint32(0); i < count; i++ {
		// Scan forward two bytes at a time looking for a UTF-16 null terminator.
		end := pos
		for end+1 < len(data) {
			if data[end] == 0 && data[end+1] == 0 {
				break
			}
			end += 2
		}
		// If end+1 is out of range the null terminator was never found.
		if end+1 >= len(data) {
			return nil, fmt.Errorf("unterminated string %d in APPEXECLINK data", i)
		}
		length := (end - pos) / 2
		units := make([]uint16, length)
		for k := range units {
			units[k] = binary.LittleEndian.Uint16(data[pos+k*2:])
		}
		out = append(out, string(utf16.Decode(units)))
		pos = end + 2 // skip past the null terminator
	}
	return out, nil
}

