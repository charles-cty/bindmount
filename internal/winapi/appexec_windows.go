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

// ReadAppExecLink opens path with FILE_FLAG_OPEN_REPARSE_POINT and reads its
// APPEXECLINK reparse data. It returns the real executable path encoded as
// string index 2 in the reparse payload.
//
// The caller should treat a non-nil error as "not an APPEXECLINK or
// unreadable"; all such files can be safely skipped when building the alias
// map.
func ReadAppExecLink(path string) (string, error) {
	p16, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}

	// Open without following the reparse point so we get the alias node.
	h, err := syscall.CreateFile(
		p16,
		0, // no data access; IOCTL only
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil,
		syscall.OPEN_EXISTING,
		syscall.FILE_FLAG_BACKUP_SEMANTICS|fileOpenReparsePoint,
		0,
	)
	if err != nil {
		return "", fmt.Errorf("open reparse point %q: %w", path, err)
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
		return "", fmt.Errorf("FSCTL_GET_REPARSE_POINT on %q: %w", path, err)
	}
	if returned < appExecLinkStringsOffset {
		return "", fmt.Errorf("%q: reparse buffer too small (%d bytes)", path, returned)
	}

	tag := binary.LittleEndian.Uint32(buf[0:4])
	if tag != ioReparseTagAppExecLink {
		return "", fmt.Errorf("%q: reparse tag 0x%08X is not APPEXECLINK", path, tag)
	}

	count := binary.LittleEndian.Uint32(buf[appExecLinkCountOffset : appExecLinkCountOffset+4])
	if count <= appExecLinkExeStringIdx {
		return "", fmt.Errorf("%q: APPEXECLINK has only %d strings, need at least %d",
			path, count, appExecLinkExeStringIdx+1)
	}

	return parseAppExecLinkStrings(buf[appExecLinkStringsOffset:returned], count, appExecLinkExeStringIdx)
}

// parseAppExecLinkStrings walks the null-terminated UTF-16LE string list
// starting at data and returns the string at targetIdx.
func parseAppExecLinkStrings(data []byte, count uint32, targetIdx int) (string, error) {
	pos := 0
	for i := 0; i < int(count); i++ {
		// Find the UTF-16 null terminator (two consecutive zero bytes,
		// aligned to a 2-byte boundary from pos).
		end := pos
		for end+1 < len(data) {
			if data[end] == 0 && data[end+1] == 0 {
				break
			}
			end += 2
		}
		if end+1 >= len(data) && !(data[end] == 0 && data[end+1] == 0) {
			return "", fmt.Errorf("unterminated string %d in APPEXECLINK data", i)
		}

		if i == targetIdx {
			length := (end - pos) / 2
			units := make([]uint16, length)
			for k := range units {
				units[k] = binary.LittleEndian.Uint16(data[pos+k*2:])
			}
			return string(utf16.Decode(units)), nil
		}

		// Advance past the null terminator (2 bytes).
		pos = end + 2
		if pos > len(data) {
			return "", fmt.Errorf("APPEXECLINK string list truncated at index %d", i)
		}
	}
	return "", fmt.Errorf("target string index %d not reached in APPEXECLINK data", targetIdx)
}

