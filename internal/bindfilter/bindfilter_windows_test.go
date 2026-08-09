//go:build windows

package bindfilter

import (
	"testing"
)

// These tests validate the offline-checkable parts of the package: option
// handling and argument validation. Tests that touch bindfltapi.dll are gated
// behind the availability check and skipped on machines without the driver.

func TestOptionsSetupFlags(t *testing.T) {
	cases := []struct {
		name  string
		opts  Options
		scope uint32
		want  uint32
	}{
		{"global plain", Options{}, 0x40, 0x40}, // NO_MULTIPLE_TARGETS
		{"global ro", Options{ReadOnly: true}, 0x40, 0x41},
		{"global merged", Options{Merged: true}, 0x40, 0x42},
		{"silo", Options{}, 0x04, 0x04},
		{"silo ro merged", Options{ReadOnly: true, Merged: true}, 0x04, 0x07},
		{"global ro merged", Options{ReadOnly: true, Merged: true}, 0x40, 0x43},
		{"global merged only", Options{Merged: true}, 0x40, 0x42},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.opts.setupFlags(c.scope)
			if got != c.want {
				t.Fatalf("setupFlags(0x%X) = 0x%X, want 0x%X", c.scope, got, c.want)
			}
		})
	}
}

func TestCreateGlobalValidatesArgs(t *testing.T) {
	if err := CreateGlobal("", "target", Options{}); err == nil {
		t.Fatal("expected error for empty virtual root")
	}
	if err := CreateGlobal("root", "", Options{}); err == nil {
		t.Fatal("expected error for empty target")
	}
}

func TestCreateSiloRequiresJob(t *testing.T) {
	if err := CreateSilo(0, `C:\v`, `C:\t`, Options{}); err == nil {
		t.Fatal("expected error for zero job handle")
	}
}

func TestListSiloRequiresJob(t *testing.T) {
	if _, err := ListSilo(0); err == nil {
		t.Fatal("expected error for zero job handle")
	}
}

func TestListVolumeRequiresPath(t *testing.T) {
	if _, err := ListVolume(""); err == nil {
		t.Fatal("expected error for empty volume path")
	}
}

func TestRemoveGlobalValidatesArgs(t *testing.T) {
	if err := RemoveGlobal(""); err == nil {
		t.Fatal("expected error for empty virtual root")
	}
}

func TestRemoveSiloValidatesArgs(t *testing.T) {
	if err := RemoveSilo(0, `C:\v`); err == nil {
		t.Fatal("expected error for zero job handle")
	}
}

func TestIsFileLevelMappingEdgeCases(t *testing.T) {
	cases := []struct {
		name        string
		virtualRoot string
		target      string
		want        bool
	}{
		// Multiple dots — only the last component matters.
		{"multiple dots in name", `C:\foo\bar.bak.exe`, `C:\baz\other.exe`, true},
		// Dotfile (leading dot, no further extension) — not a file extension.
		{"dotfile no ext", `C:\foo\.gitignore`, `C:\bar\.gitignore`, false},
		// Path with spaces.
		{"spaces in path", `C:\Program Files\app\tool.exe`, `C:\Backup\tool.exe`, true},
		// Extension on intermediate component, not the last.
		{"ext only on intermediate", `C:\foo.d\bar`, `C:\baz.d\bin`, false},
		// Empty paths — no extension, treated as directory.
		{"empty paths", "", "", false},
		// Target is a bare volume root.
		{"target volume root", `C:\foo\bar.exe`, `C:\`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := isFileLevelMapping(c.virtualRoot, c.target)
			if got != c.want {
				t.Fatalf("isFileLevelMapping(%q, %q) = %v, want %v",
					c.virtualRoot, c.target, got, c.want)
			}
		})
	}
}

func TestIsFileLevelMapping(t *testing.T) {
	cases := []struct {
		name        string
		virtualRoot string
		target      string
		want        bool
	}{
		// Target has a file extension — detected via heuristic when stat fails
		// (stat will fail because these paths don't exist on the test machine).
		{"exe target", `C:\foo\bar.exe`, `C:\baz\other.exe`, true},
		{"dll target", `C:\foo\helper`, `C:\baz\lib.dll`, true},
		// Only the virtual root has an extension and target doesn't — still
		// treated as file-level because either path triggers the heuristic.
		{"root has ext, target dir-like", `C:\foo\bar.exe`, `C:\baz\bin`, true},
		// Neither path has an extension — treated as directory mapping.
		{"both dir-like", `C:\foo\bar`, `C:\baz\bin`, false},
		// Bare directory paths.
		{"directories with slash", `C:\foo\`, `C:\bar\`, false},
		// Single-component file names.
		{"single component file", `bar.exe`, `other.exe`, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := isFileLevelMapping(c.virtualRoot, c.target)
			if got != c.want {
				t.Fatalf("isFileLevelMapping(%q, %q) = %v, want %v",
					c.virtualRoot, c.target, got, c.want)
			}
		})
	}
}
