//go:build windows

package winapi

import (
	"encoding/binary"
	"testing"
)

// buildResponse builds a well-formed BfGetMappings response for one mapping
// with the given virtual root and targets. The layout matches the documented
// format: header, entries, target entries, then strings.
func buildResponse(virtRoot string, flags uint32, targets []string) []byte {
	virt := utf16Units(virtRoot)
	targetUnits := make([][]uint16, len(targets))
	totalTargetLen := 0
	for i, t := range targets {
		targetUnits[i] = utf16Units(t)
		totalTargetLen += len(targetUnits[i]) * 2
	}

	headerSize := 12
	entrySize := 20
	targetsSize := len(targets) * 8
	stringsOff := headerSize + entrySize + targetsSize
	total := stringsOff + len(virt)*2 + totalTargetLen

	buf := make([]byte, total)
	binary.LittleEndian.PutUint32(buf[0:], uint32(total)) // Size
	binary.LittleEndian.PutUint32(buf[4:], 0)             // Status
	binary.LittleEndian.PutUint32(buf[8:], 1)             // MappingCount

	entryOff := headerSize
	binary.LittleEndian.PutUint32(buf[entryOff+0:], uint32(len(virt)*2))
	binary.LittleEndian.PutUint32(buf[entryOff+4:], uint32(stringsOff))
	binary.LittleEndian.PutUint32(buf[entryOff+8:], flags)
	binary.LittleEndian.PutUint32(buf[entryOff+12:], uint32(len(targets)))
	binary.LittleEndian.PutUint32(buf[entryOff+16:], uint32(headerSize+entrySize))

	// Virtual root string.
	strOff := stringsOff
	for i, u := range virt {
		binary.LittleEndian.PutUint16(buf[strOff+i*2:], u)
	}
	strOff += len(virt) * 2

	// Target entries + strings.
	tgtEntryOff := headerSize + entrySize
	for i, tu := range targetUnits {
		binary.LittleEndian.PutUint32(buf[tgtEntryOff+i*8+0:], uint32(len(tu)*2))
		binary.LittleEndian.PutUint32(buf[tgtEntryOff+i*8+4:], uint32(strOff))
		for j, u := range tu {
			binary.LittleEndian.PutUint16(buf[strOff+j*2:], u)
		}
		strOff += len(tu) * 2
	}
	return buf
}

func utf16Units(s string) []uint16 {
	// Simple BMP-only helper for test strings.
	units := make([]uint16, 0, len(s))
	for _, r := range s {
		if r > 0xFFFF {
			// surrogate pair
			r -= 0x10000
			units = append(units, uint16(0xD800+(r>>10)), uint16(0xDC00+(r&0x3FF)))
		} else {
			units = append(units, uint16(r))
		}
	}
	return units
}

func TestParseMappingsCheckedValid(t *testing.T) {
	buf := buildResponse(`\Device\HarddiskVolume3\Temp\virt`, 0x41, []string{
		`\Device\HarddiskVolume3\Temp\backing`,
	})

	ms, err := ParseMappingsChecked(buf, uint32(len(buf)))
	if err != nil {
		t.Fatalf("ParseMappingsChecked: %v", err)
	}
	if len(ms) != 1 {
		t.Fatalf("got %d mappings, want 1", len(ms))
	}
	m := ms[0]
	if m.VirtualRoot != `\Device\HarddiskVolume3\Temp\virt` {
		t.Errorf("virtual root = %q", m.VirtualRoot)
	}
	if m.Flags != 0x41 {
		t.Errorf("flags = 0x%X", m.Flags)
	}
	if len(m.Targets) != 1 || m.Targets[0] != `\Device\HarddiskVolume3\Temp\backing` {
		t.Errorf("targets = %v", m.Targets)
	}
}

func TestParseMappingsCheckedMultiTarget(t *testing.T) {
	buf := buildResponse(`\Device\HarddiskVolume3\v`, 0x02, []string{
		`\Device\HarddiskVolume3\backing`,
		`\Device\HarddiskVolume3\v`,
	})
	ms, err := ParseMappingsChecked(buf, uint32(len(buf)))
	if err != nil {
		t.Fatalf("ParseMappingsChecked: %v", err)
	}
	if len(ms) != 1 || len(ms[0].Targets) != 2 {
		t.Fatalf("got %+v", ms)
	}
}

func TestParseMappingsCheckedRejectsTruncated(t *testing.T) {
	buf := buildResponse(`\Device\HarddiskVolume3\Temp\virt`, 0, []string{`\Device\HarddiskVolume3\Temp\b`})

	// Truncate inside the string area.
	if _, err := ParseMappingsChecked(buf, uint32(len(buf)-10)); err == nil {
		t.Fatal("expected error for truncated buffer")
	}
	// Truncate inside the header.
	if _, err := ParseMappingsChecked(buf[:8], 8); err == nil {
		t.Fatal("expected error for short header")
	}
}

func TestParseMappingsCheckedRejectsBadCount(t *testing.T) {
	buf := buildResponse(`\Device\HarddiskVolume3\v`, 0, nil)
	binary.LittleEndian.PutUint32(buf[8:], 0xFFFFFFFF) // absurd MappingCount
	if _, err := ParseMappingsChecked(buf, uint32(len(buf))); err == nil {
		t.Fatal("expected error for absurd mapping count")
	}
}

func TestParseMappingsCheckedRejectsBadOffset(t *testing.T) {
	buf := buildResponse(`\Device\HarddiskVolume3\v`, 0, []string{`\Device\HarddiskVolume3\b`})
	// Corrupt the virtual root offset to point past the end.
	binary.LittleEndian.PutUint32(buf[16:], uint32(len(buf)+100))
	if _, err := ParseMappingsChecked(buf, uint32(len(buf))); err == nil {
		t.Fatal("expected error for out-of-range virtual root offset")
	}
}

func TestParseMappingsCheckedRejectsNonZeroStatus(t *testing.T) {
	buf := buildResponse(`\Device\HarddiskVolume3\v`, 0, nil)
	binary.LittleEndian.PutUint32(buf[4:], 0xC0000001)
	if _, err := ParseMappingsChecked(buf, uint32(len(buf))); err == nil {
		t.Fatal("expected error for non-zero filter status")
	}
}
