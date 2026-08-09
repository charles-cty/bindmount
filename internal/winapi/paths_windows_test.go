//go:build windows

package winapi

import "testing"

func TestNTVirtualRootToDOS(t *testing.T) {
	// Find the drive letter of the system volume so the test is not tied to
	// C: specifically (it almost always is C:, but the assertion below only
	// requires internal consistency).
	sysDrive := ""
	for letter := 'A'; letter <= 'Z'; letter++ {
		drive := string(letter) + ":"
		if _, err := queryDosDevice(drive); err == nil {
			sysDrive = drive
			break
		}
	}
	if sysDrive == "" {
		t.Skip("no drive letters found")
	}

	dev, err := queryDosDevice(sysDrive)
	if err != nil {
		t.Fatalf("queryDosDevice(%q): %v", sysDrive, err)
	}

	got := NTVirtualRootToDOS(dev + `\Temp\virt`)
	want := sysDrive + `\Temp\virt`
	if got != want {
		t.Errorf("NTVirtualRootToDOS(%q) = %q, want %q", dev+`\Temp\virt`, got, want)
	}
}

func TestNTVirtualRootToDOSFallback(t *testing.T) {
	// A non-volume NT path must come back unchanged.
	in := `\Device\NoSuchVolume9999\x`
	if got := NTVirtualRootToDOS(in); got != in {
		t.Errorf("NTVirtualRootToDOS(%q) = %q, want unchanged", in, got)
	}
	// A non-NT path likewise.
	in2 := `C:\already\dos`
	if got := NTVirtualRootToDOS(in2); got != in2 {
		t.Errorf("NTVirtualRootToDOS(%q) = %q, want unchanged", in2, got)
	}
}

func TestNTVirtualRootToDOSTrailingBackslash(t *testing.T) {
	sysDrive, dev := firstDrive(t)
	// Trailing backslash must be preserved round-trip.
	in := dev + `\Temp\`
	got := NTVirtualRootToDOS(in)
	want := sysDrive + `\Temp\`
	if got != want {
		t.Errorf("NTVirtualRootToDOS(%q) = %q, want %q", in, got, want)
	}
}

func TestNTVirtualRootToDOSVolumeRootOnly(t *testing.T) {
	sysDrive, dev := firstDrive(t)
	// A path that is just the volume device with no subpath.
	got := NTVirtualRootToDOS(dev)
	want := sysDrive
	if got != want {
		t.Errorf("NTVirtualRootToDOS(%q) = %q, want %q", dev, got, want)
	}
}

func TestNTVirtualRootToDOSAllMappedDrives(t *testing.T) {
	// Every drive that QueryDosDevice resolves must round-trip through
	// NTVirtualRootToDOS correctly.
	for letter := 'A'; letter <= 'Z'; letter++ {
		drive := string(letter) + ":"
		dev, err := queryDosDevice(drive)
		if err != nil {
			continue // drive not present
		}
		got := NTVirtualRootToDOS(dev + `\sub`)
		want := drive + `\sub`
		if got != want {
			t.Errorf("drive %s: NTVirtualRootToDOS(%q) = %q, want %q",
				drive, dev+`\sub`, got, want)
		}
	}
}

// firstDrive returns the first resolvable drive letter and its NT device path,
// skipping the test if none is found.
func firstDrive(t *testing.T) (drive, dev string) {
	t.Helper()
	for letter := 'A'; letter <= 'Z'; letter++ {
		d := string(letter) + ":"
		if nt, err := queryDosDevice(d); err == nil {
			return d, nt
		}
	}
	t.Skip("no drive letters found")
	return "", ""
}
