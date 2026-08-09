//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"bindmount/internal/bindfilter"
	"bindmount/internal/winapi"
)

// TestSiloScopedBindLinkIntegration exercises the complete contract that is
// otherwise difficult to validate with unit tests: a promoted Job Silo gets a
// scoped Bind Link, a process launched into that silo observes the target, and
// the host process still observes the original virtual file.
func TestSiloScopedBindLinkIntegration(t *testing.T) {
	root := t.TempDir()
	virtual := filepath.Join(root, "virtual")
	target := filepath.Join(root, "target")
	if err := os.MkdirAll(virtual, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	const name = "virtual.txt"
	if err := os.WriteFile(filepath.Join(virtual, name), []byte("virtual"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, name), []byte("backing"), 0o644); err != nil {
		t.Fatal(err)
	}

	job, err := winapi.CreateJob("")
	if err != nil {
		t.Skipf("Job Objects unavailable: %v", err)
	}
	defer syscall.CloseHandle(job)
	if err := winapi.SetJobLimitFlags(job, winapi.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE); err != nil {
		t.Skipf("cannot configure Job Object (elevation/policy): %v", err)
	}
	if err := winapi.PromoteToSilo(job); err != nil {
		t.Skipf("Job Silo unavailable on this Windows build: %v", err)
	}

	// Scope the mapping over the existing virtual directory. A host process
	// must continue to see its original contents while the silo sees target.
	virtualRoot := virtual
	if err := bindfilter.CreateSilo(job, virtualRoot, target, bindfilter.Options{}); err != nil {
		t.Skipf("Bind Filter silo mappings unavailable (driver/elevation): %v", err)
	}
	defer bindfilter.RemoveSilo(job, virtualRoot)

	mappings, err := bindfilter.ListSilo(job)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, mapping := range mappings {
		// BfGetMappings may return the DOS short (8.3) spelling while the
		// test setup used the long spelling. The mapping's unique final
		// component is sufficient here, and the target is checked below.
		if strings.HasSuffix(strings.ToLower(filepath.Clean(mapping.VirtualRoot)), `\virtual`) &&
			len(mapping.Targets) == 1 && strings.HasSuffix(strings.ToLower(filepath.Clean(mapping.Targets[0])), `\target`) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("silo mapping %q was not returned by ListSilo: %#v", virtualRoot, mappings)
	}

	if got, err := os.ReadFile(filepath.Join(virtualRoot, name)); err != nil {
		t.Fatal(err)
	} else if string(got) != "virtual" {
		t.Fatalf("host unexpectedly observed silo mapping: got %q", got)
	}

	probePath := filepath.Join(virtualRoot, name)
	probe := "if ((Get-Content -Raw -LiteralPath '" + strings.ReplaceAll(probePath, "'", "''") + "').Trim() -ne 'backing') { exit 1 }"
	exitCode, err := runInSilo(job, []string{"pwsh.exe", "-Command", probe}, false, "")
	if err != nil {
		// Silo job-list attributes are rejected on some Windows builds; use
		// the same suspended-create fallback as cmdExec in that case.
		exitCode, err = runInSiloFallback(job, []string{"pwsh.exe", "-Command", probe}, false)
		if err != nil {
			t.Fatalf("launch process in silo: %v", err)
		}
	}
	if exitCode != 0 {
		t.Fatalf("silo probe exited with code %d", exitCode)
	}
}
