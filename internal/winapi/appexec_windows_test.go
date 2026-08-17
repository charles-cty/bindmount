//go:build windows

package winapi

import (
	"encoding/binary"
	"testing"
	"unicode/utf16"
)

// buildAppExecLinkPayload builds the string-list payload of an APPEXECLINK
// reparse buffer (everything after the 16-byte header) from the given
// strings, each null-terminated in UTF-16LE.
func buildAppExecLinkPayload(strings ...string) []byte {
	var out []byte
	for _, s := range strings {
		for _, u := range utf16.Encode([]rune(s)) {
			var b [2]byte
			binary.LittleEndian.PutUint16(b[:], u)
			out = append(out, b[:]...)
		}
		out = append(out, 0, 0)
	}
	return out
}

func TestParseAllAppExecLinkStringsTypical(t *testing.T) {
	payload := buildAppExecLinkPayload(
		"Microsoft.DesktopAppInstaller_8wekyb3d8bbwe",
		"Microsoft.DesktopAppInstaller_8wekyb3d8bbwe!winget",
		`C:\Program Files\WindowsApps\Microsoft.DesktopAppInstaller_1.29.280.0_x64__8wekyb3d8bbwe\winget.exe`,
		"0",
	)
	got := parseAllAppExecLinkStrings(payload)
	if len(got) != 4 {
		t.Fatalf("got %d strings, want 4", len(got))
	}
	if got[2] != `C:\Program Files\WindowsApps\Microsoft.DesktopAppInstaller_1.29.280.0_x64__8wekyb3d8bbwe\winget.exe` {
		t.Errorf("string[2] = %q", got[2])
	}
	info := appExecLinkInfoFromStrings(got)
	if info.PackageFamilyName != "Microsoft.DesktopAppInstaller_8wekyb3d8bbwe" {
		t.Errorf("package family name = %q", info.PackageFamilyName)
	}
}

// Regression: on build 26100 the StringCount field of the reparse buffer was
// observed to contain the first bytes of string[0] rather than the real
// count. The parser must ignore the count and scan by shape, recovering all
// four strings.
func TestParseAllAppExecLinkStringsIgnoresCorruptCount(t *testing.T) {
	payload := buildAppExecLinkPayload(
		"MicrosoftCorporationII.WindowsSubsystemForLinux_8wekyb3d8bbwe",
		"MicrosoftCorporationII.WindowsSubsystemForLinux_8wekyb3d8bbwe!wsl",
		`C:\Program Files\WindowsApps\MicrosoftCorporationII.WindowsSubsystemForLinux_2.7.3.0_x64__8wekyb3d8bbwe\wsl.exe`,
		"0",
	)
	// Simulate the corrupted header: first bytes of string[0] land where the
	// count would be read. The parser no longer receives a count at all, so
	// this test simply verifies the payload alone yields all four strings.
	got := parseAllAppExecLinkStrings(payload)
	if len(got) != 4 {
		t.Fatalf("got %d strings, want 4", len(got))
	}
	if got[0] != "MicrosoftCorporationII.WindowsSubsystemForLinux_8wekyb3d8bbwe" {
		t.Errorf("string[0] = %q", got[0])
	}
}

// A payload that ends mid-string (no trailing null) must not error: the
// partial tail simply marks the end of the list.
func TestParseAllAppExecLinkStringsTruncatedTail(t *testing.T) {
	payload := buildAppExecLinkPayload("pkg", "entry", `C:\x\y.exe`, "0")
	// Chop the final null terminator off, leaving "0" unterminated.
	payload = payload[:len(payload)-2]
	got := parseAllAppExecLinkStrings(payload)
	if len(got) != 3 {
		t.Fatalf("got %d strings, want 3 (unterminated tail dropped)", len(got))
	}
	if got[2] != `C:\x\y.exe` {
		t.Errorf("string[2] = %q", got[2])
	}
}
