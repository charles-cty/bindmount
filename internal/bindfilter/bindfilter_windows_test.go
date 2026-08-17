//go:build windows

package bindfilter

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"bindmount/internal/winapi"
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

func TestTargetFileLevel(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "release.1")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "no-extension")
	if err := os.WriteFile(file, []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got, known := targetFileLevel(dir); got || !known {
		t.Fatalf("dotted directory classified as file=%v known=%v", got, known)
	}
	if got, known := targetFileLevel(file); !got || !known {
		t.Fatalf("extensionless file classified as file=%v known=%v", got, known)
	}
	if got, known := targetFileLevel(filepath.Join(t.TempDir(), "missing.exe")); got || known {
		t.Fatalf("missing dotted target classified as file=%v known=%v", got, known)
	}
}

func TestSetupMappingRetriesUnknownTargetAsFile(t *testing.T) {
	firstErr := errors.New("directory mapping rejected")
	var calls []uint32
	setup := func(_ winapi.Handle, flags uint32, _, _ string, _ []string) error {
		calls = append(calls, flags)
		if len(calls) == 1 {
			return firstErr
		}
		return nil
	}

	baseFlags := uint32(winapi.BINDFLT_FLAG_USE_CURRENT_SILO_MAPPING)
	err := setupMapping(setup, 1, baseFlags, `C:\virtual\tool.exe`, `C:\missing\tool.exe`, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 {
		t.Fatalf("setup calls = %d, want 2", len(calls))
	}
	if calls[0] != baseFlags {
		t.Fatalf("first flags = 0x%X, want 0x%X", calls[0], baseFlags)
	}
	wantRetry := baseFlags | winapi.BINDFLT_FLAG_REPARSE_ON_FILES
	if calls[1] != wantRetry {
		t.Fatalf("retry flags = 0x%X, want 0x%X", calls[1], wantRetry)
	}
}

func TestSetupMappingDoesNotRetryKnownDirectory(t *testing.T) {
	target := filepath.Join(t.TempDir(), "release.1")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("setup failed")
	calls := 0
	setup := func(_ winapi.Handle, flags uint32, _, _ string, _ []string) error {
		calls++
		if flags&winapi.BINDFLT_FLAG_REPARSE_ON_FILES != 0 {
			t.Fatalf("known directory received REPARSE_ON_FILES: 0x%X", flags)
		}
		return wantErr
	}

	err := setupMapping(setup, 1, 0, `C:\virtual\release.1`, target, nil)
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
	if calls != 1 {
		t.Fatalf("setup calls = %d, want 1", calls)
	}
}

func TestNextMappingsBufferSize(t *testing.T) {
	cases := []struct {
		name     string
		current  int
		reported uint32
		want     int
		wantErr  bool
	}{
		{"uses reported size", 64 << 10, 200 << 10, 200 << 10, false},
		{"doubles stale size", 64 << 10, 64 << 10, 128 << 10, false},
		{"allows exact limit", 8 << 20, maxMappingsBufferSize, maxMappingsBufferSize, false},
		{"rejects huge report", 64 << 10, ^uint32(0), 0, true},
		{"rejects doubling past limit", maxMappingsBufferSize, maxMappingsBufferSize, 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := nextMappingsBufferSize(c.current, c.reported)
			if (err != nil) != c.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, c.wantErr)
			}
			if got != c.want {
				t.Fatalf("size = %d, want %d", got, c.want)
			}
		})
	}
}
