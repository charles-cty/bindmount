//go:build windows

package winapi

import (
	"encoding/binary"
	"fmt"
	"unicode/utf16"
)

// Mapping describes one virtual root with its flags and backing targets, as
// decoded from the BfGetMappings response. Targets holds the raw NT device
// paths as returned by the filter; conversion to DOS paths is performed by
// the caller when needed.
type Mapping struct {
	VirtualRoot string
	Flags       uint32
	Targets     []string
}

// Binary layout of the undocumented response; see
// docs/BindFilterAPI.md#bfgetmappings-buffer-format. All fields are little
// endian uint32; offsets are relative to the start of the buffer.
const (
	mappingsHeaderSize = 12 // Size, Status, MappingCount
	mappingEntrySize   = 20 // 5 * uint32
	targetEntrySize    = 8  // 2 * uint32
	maxMappingCount    = 1 << 16
	maxTargetCount     = 1 << 12
)

// ParseMappingsChecked decodes the BfGetMappings response buffer, validating
// every offset and length before dereferencing it, per the checklist in
// docs/BindFilterAPI.md. size is the value returned in BufferSize.
func ParseMappingsChecked(buf []byte, size uint32) ([]Mapping, error) {
	if size > uint32(len(buf)) {
		return nil, fmt.Errorf("reported size %d exceeds buffer length %d", size, len(buf))
	}
	buf = buf[:size]

	if len(buf) < mappingsHeaderSize {
		return nil, fmt.Errorf("buffer too small for response header (%d bytes)", len(buf))
	}

	hdrSize := binary.LittleEndian.Uint32(buf[0:4])
	status := binary.LittleEndian.Uint32(buf[4:8])
	count := binary.LittleEndian.Uint32(buf[8:12])

	// The filter-supplied status is not a documented Win32 code, but the
	// observed zero value accompanies successful responses; anything else is
	// surfaced to the caller for diagnosis rather than silently accepted.
	if status != 0 {
		return nil, fmt.Errorf("bind filter returned status 0x%08X in mappings response", status)
	}
	if hdrSize < mappingsHeaderSize || hdrSize > uint32(len(buf)) {
		return nil, fmt.Errorf("implausible response size field %d (buffer %d bytes)", hdrSize, len(buf))
	}
	if count > maxMappingCount {
		return nil, fmt.Errorf("implausible mapping count %d", count)
	}
	entriesEnd := uint64(mappingsHeaderSize) + uint64(count)*mappingEntrySize
	if entriesEnd > uint64(len(buf)) {
		return nil, fmt.Errorf("mapping entry array (%d entries) exceeds buffer (%d bytes)", count, len(buf))
	}

	mappings := make([]Mapping, 0, count)
	for i := uint32(0); i < count; i++ {
		base := mappingsHeaderSize + i*mappingEntrySize
		virtLen := binary.LittleEndian.Uint32(buf[base+0:])
		virtOff := binary.LittleEndian.Uint32(buf[base+4:])
		flags := binary.LittleEndian.Uint32(buf[base+8:])
		numTargets := binary.LittleEndian.Uint32(buf[base+12:])
		targetsOff := binary.LittleEndian.Uint32(buf[base+16:])

		virt, err := readUTF16(buf, virtOff, virtLen)
		if err != nil {
			return nil, fmt.Errorf("mapping %d virtual root: %w", i, err)
		}

		if numTargets > maxTargetCount {
			return nil, fmt.Errorf("mapping %d: implausible target count %d", i, numTargets)
		}
		targetsEnd := uint64(targetsOff) + uint64(numTargets)*targetEntrySize
		if targetsEnd > uint64(len(buf)) {
			return nil, fmt.Errorf("mapping %d: target entry array exceeds buffer", i)
		}

		targets := make([]string, 0, numTargets)
		for j := uint32(0); j < numTargets; j++ {
			tbase := targetsOff + j*targetEntrySize
			tLen := binary.LittleEndian.Uint32(buf[tbase+0:])
			tOff := binary.LittleEndian.Uint32(buf[tbase+4:])
			t, err := readUTF16(buf, tOff, tLen)
			if err != nil {
				return nil, fmt.Errorf("mapping %d target %d: %w", i, j, err)
			}
			targets = append(targets, t)
		}

		mappings = append(mappings, Mapping{VirtualRoot: virt, Flags: flags, Targets: targets})
	}
	return mappings, nil
}

// readUTF16 reads a length-prefixed UTF-16 string from buf. off and len are in
// bytes, per the wire format. The string is handled purely by its explicit
// byte length; a NUL terminator is neither required nor trusted.
func readUTF16(buf []byte, off, length uint32) (string, error) {
	if length%2 != 0 {
		return "", fmt.Errorf("odd UTF-16 byte length %d", length)
	}
	end := uint64(off) + uint64(length)
	if end > uint64(len(buf)) {
		return "", fmt.Errorf("string at offset %d length %d exceeds buffer (%d bytes)", off, length, len(buf))
	}
	units := make([]uint16, length/2)
	src := buf[off:end]
	for k := range units {
		units[k] = binary.LittleEndian.Uint16(src[k*2:])
	}
	return string(utf16.Decode(units)), nil
}

// SID is an opaque security identifier; only its address is passed through to
// BfGetMappings. Construction from a token or string form is not yet
// implemented because the CLI's silo and volume queries do not need it.
type SID struct {
	_ [0]func() // unconstructable for now
}
