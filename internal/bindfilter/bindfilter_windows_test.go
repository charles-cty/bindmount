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
