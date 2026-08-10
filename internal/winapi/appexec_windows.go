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
//	[8:12]  ULONG  Version           (only when the header is present)
//	[12:16] ULONG  StringCount       (only when the header is present)
//	[12… or 16…]  null-terminated UTF-16LE strings, concatenated:
//	          [0] Package family name (for activation)
//	          [1] Entry point (AppUserModelId)
//	          [2] Executable path        ← the value we want
//	          [3] Application type flag  ("0" for regular UWP)
//
// On build 26100 the observed buffer carries no Version/StringCount header
// at all: the string payload begins at offset 12, immediately after
// ReparseTag+ReparseDataLength+Reserved. The bytes at [12:16] that older
// documentation calls StringCount are actually the first characters of
// string[0] ("M","i" read as a uint32). The parser therefore does not trust
// a fixed offset or a count; it scans for the string list by shape, starting
// at offset 12, and falls back to offset 16 for layouts that do carry the
// 8-byte version+count header.
const (
	appExecLinkHeaderSize = 8 // ReparseTag + ReparseDataLength + Reserved
	// appExecLinkStringsOffsetV3 is where the string list begins on the
	// build-26100 layout (no version/count header).
	appExecLinkStringsOffsetV3 = 12
	// appExecLinkStringsOffsetV1 is where it begins when the 8-byte
	// Version+StringCount header is present.
	appExecLinkStringsOffsetV1 = 16
	appExecLinkExeStringIdx    = 2 // index of the executable path in the list
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
	if returned < appExecLinkStringsOffsetV3 {
		return nil, fmt.Errorf("%q: reparse buffer too small (%d bytes)", path, returned)
	}
	tag := binary.LittleEndian.Uint32(buf[0:4])
	if tag != ioReparseTagAppExecLink {
		return nil, fmt.Errorf("%q: reparse tag 0x%08X is not APPEXECLINK", path, tag)
	}

	// Scan the payload for null-terminated strings by shape, trying the
	// headerless (offset 12) layout first and falling back to the
	// version+count (offset 16) layout. See the layout comment above.
	strings, err := parseAllAppExecLinkStrings(buf[appExecLinkStringsOffsetV3:returned])
	if err != nil || len(strings) <= appExecLinkExeStringIdx {
		strings, err = parseAllAppExecLinkStrings(buf[appExecLinkStringsOffsetV1:returned])
	}
	if err != nil {
		return nil, err
	}
	if len(strings) <= appExecLinkExeStringIdx {
		return nil, fmt.Errorf("%q: APPEXECLINK has only %d strings, need at least %d",
			path, len(strings), appExecLinkExeStringIdx+1)
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

// parseAllAppExecLinkStrings decodes the null-terminated UTF-16LE string
// list in the APPEXECLINK payload and returns every string found. Scanning
// stops at the first unterminated or empty entry; the documented layout has
// exactly four strings followed by no payload, so a trailing partial read
// marks the end of the list rather than a format error.
func parseAllAppExecLinkStrings(data []byte) ([]string, error) {
	var out []string
	pos := 0
	for pos+1 < len(data) {
		// Scan forward two bytes at a time looking for a UTF-16 null terminator.
		end := pos
		for end+1 < len(data) {
			if data[end] == 0 && data[end+1] == 0 {
				break
			}
			end += 2
		}
		// If end+1 is out of range the null terminator was never found:
		// that is the end of the string list, not an error.
		if end+1 >= len(data) {
			break
		}
		if end == pos {
			// Empty entry: the string list is over.
			break
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
