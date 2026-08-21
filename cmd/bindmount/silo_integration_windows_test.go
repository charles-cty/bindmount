//go:build windows

package main

import (
	"fmt"
	"io"
	"os"
	osExec "os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"unsafe"

	"bindmount/internal/bindfilter"
	"bindmount/internal/winapi"
)

func TestSiloFindResolvesNamedSiloForPID(t *testing.T) {
	name := fmt.Sprintf("bindmount-find-%d", syscall.Getpid())
	job, err := winapi.CreateJob(name)
	if err != nil {
		t.Skipf("create named job: %v", err)
	}
	defer syscall.CloseHandle(job)
	if err := winapi.SetKillOnJobClose(job); err != nil {
		t.Skipf("configure job: %v", err)
	}
	if err := winapi.PromoteToSilo(job); err != nil {
		t.Skipf("promote job to silo: %v", err)
	}
	info, err := winapi.QuerySiloBasicInformation(job)
	if err != nil {
		t.Fatal(err)
	}

	commandPath := os.Getenv("ComSpec")
	if commandPath == "" {
		commandPath = `C:\Windows\System32\cmd.exe`
	}
	command := syscall.StringToUTF16Ptr(commandPath + " /c exit 0")
	var startupInfo syscall.StartupInfo
	startupInfo.Cb = uint32(unsafe.Sizeof(startupInfo))
	var processInfo syscall.ProcessInformation
	if err := syscall.CreateProcess(nil, command, nil, nil, false, winapi.CREATE_SUSPENDED, nil, nil, &startupInfo, &processInfo); err != nil {
		t.Fatal(err)
	}
	defer syscall.CloseHandle(processInfo.Thread)
	defer syscall.CloseHandle(processInfo.Process)
	defer syscall.TerminateProcess(processInfo.Process, 1)
	if err := winapi.AssignProcessToJob(job, processInfo.Process); err != nil {
		t.Fatal(err)
	}

	oldStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	findErr := cmdSiloFind(strconv.FormatUint(uint64(processInfo.ProcessId), 10))
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	os.Stdout = oldStdout
	defer reader.Close()
	if findErr != nil {
		t.Fatal(findErr)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("silo %q (ID %d)", name, info.SiloID)
	if !strings.Contains(string(output), want) {
		t.Fatalf("silo find output = %q, want %q", output, want)
	}
}

func TestSiloExecLaunchesInExistingSilo(t *testing.T) {
	name := fmt.Sprintf("bindmount-enter-%d", syscall.Getpid())
	job, err := winapi.CreateJob(name)
	if err != nil {
		t.Skipf("create named job: %v", err)
	}
	defer syscall.CloseHandle(job)
	if err := winapi.SetKillOnJobClose(job); err != nil {
		t.Skipf("configure job: %v", err)
	}
	if err := winapi.PromoteToSilo(job); err != nil {
		t.Skipf("promote job to silo: %v", err)
	}

	pwshPath, err := osExec.LookPath("pwsh.exe")
	if err != nil {
		t.Fatal(err)
	}
	if err := cmdSiloExec([]string{name, "--", pwshPath, "-NoProfile", "-Command", "exit 0"}); err != nil {
		t.Fatal(err)
	}
}

func TestSiloExecRejectsNonSiloJob(t *testing.T) {
	name := fmt.Sprintf("bindmount-not-silo-%d", syscall.Getpid())
	job, err := winapi.CreateJob(name)
	if err != nil {
		t.Skipf("create named job: %v", err)
	}
	defer syscall.CloseHandle(job)

	err = cmdSiloExec([]string{name, "--", "cmd.exe", "/c", "exit 0"})
	if err == nil || !strings.Contains(err.Error(), "is not a Job Silo") {
		t.Fatalf("cmdSiloExec(non-silo) error = %v, want non-silo error", err)
	}
}

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
	if shouldFallbackSiloLaunch(err) {
		// Silo job-list attributes are rejected on some Windows builds; use
		// the same suspended-create fallback as cmdExec in that case.
		exitCode, err = runInSiloFallback(job, []string{"pwsh.exe", "-Command", probe}, false)
	}
	if err != nil {
		t.Fatalf("launch process in silo: %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("silo probe exited with code %d", exitCode)
	}
}

// TestSiloScopedBindLinkDirectorySymlinkVirtualRoot records the driver's
// behavior when a directory symbolic link is used as a Bind Filter root.
func TestSiloScopedBindLinkDirectorySymlinkVirtualRoot(t *testing.T) {
	root := t.TempDir()
	physical := filepath.Join(root, "physical")
	target := filepath.Join(root, "target")
	alias := filepath.Join(root, "nodejs")
	for _, path := range []string{physical, target} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(physical, alias); err != nil {
		t.Skipf("create directory symbolic link: %v", err)
	}
	const name = "node.exe"
	if err := os.WriteFile(filepath.Join(physical, name), []byte("physical"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, name), []byte("backing"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, resolvedTarget, isLink, err := directorySymbolicLink(alias)
	if err != nil {
		t.Fatal(err)
	}
	if !isLink {
		t.Fatal("directory symbolic link was not detected")
	}
	resolvedInfo, err := os.Stat(resolvedTarget)
	if err != nil {
		t.Fatal(err)
	}
	physicalInfo, err := os.Stat(physical)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(resolvedInfo, physicalInfo) {
		t.Fatalf("resolved target = %q, want directory %q", resolvedTarget, physical)
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
	if err := bindfilter.CreateSilo(job, alias, target, bindfilter.Options{}); err != nil {
		t.Skipf("Bind Filter silo mappings unavailable (driver/elevation): %v", err)
	}
	defer bindfilter.RemoveSilo(job, alias)
	mappings, err := bindfilter.ListSilo(job)
	if err != nil {
		t.Fatal(err)
	}
	resolvedMappingTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, mapping := range mappings {
		if strings.EqualFold(filepath.Clean(mapping.VirtualRoot), filepath.Clean(resolvedTarget)) &&
			len(mapping.Targets) == 1 &&
			strings.EqualFold(filepath.Clean(mapping.Targets[0]), filepath.Clean(resolvedMappingTarget)) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("directory-link root was not recorded as %q: %#v", resolvedTarget, mappings)
	}

	if got, err := os.ReadFile(filepath.Join(alias, name)); err != nil {
		t.Fatal(err)
	} else if string(got) != "physical" {
		t.Fatalf("host unexpectedly observed silo mapping: got %q", got)
	}

	probePath := filepath.Join(alias, name)
	probe := "if ((Get-Content -Raw -LiteralPath '" + strings.ReplaceAll(probePath, "'", "''") + "').Trim() -ne 'backing') { exit 1 }"
	exitCode, err := runInSilo(job, []string{"pwsh.exe", "-Command", probe}, false, "")
	if shouldFallbackSiloLaunch(err) {
		exitCode, err = runInSiloFallback(job, []string{"pwsh.exe", "-Command", probe}, false)
	}
	if err != nil {
		t.Fatalf("launch process in silo: %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("silo probe exited with code %d", exitCode)
	}
}

func TestSiloRootPathDirectorySymbolicLink(t *testing.T) {
	hostRoot := t.TempDir()
	physical := filepath.Join(hostRoot, "versions", "v22.18.0")
	alias := filepath.Join(hostRoot, "nvm4w", "nodejs")
	if err := os.MkdirAll(physical, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(alias), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(physical, alias); err != nil {
		t.Skipf("create directory symbolic link: %v", err)
	}
	const name = "node.exe"
	if err := os.WriteFile(filepath.Join(physical, name), []byte("physical"), 0o644); err != nil {
		t.Fatal(err)
	}

	linkTarget, resolvedTarget, isLink, err := directorySymbolicLink(alias)
	if err != nil {
		t.Fatal(err)
	}
	if !isLink {
		t.Fatal("directory symbolic link was not detected")
	}
	rootDir := t.TempDir()
	stagedPath, err := stageDirectorySymbolicLink(rootDir, alias, linkTarget)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(stagedPath); err != nil {
		t.Fatal(err)
	} else if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("staged path %q is not a symbolic link", stagedPath)
	}

	volume := filepath.VolumeName(alias)
	if len(volume) != 2 || volume[1] != ':' {
		t.Skipf("test path %q is not on a drive-letter volume", alias)
	}
	rootBacking := filepath.Join(rootDir, strings.ToUpper(volume[:1]))
	if err := os.MkdirAll(rootBacking, 0o755); err != nil {
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

	driveRoot := volume + `\`
	if err := bindfilter.CreateSilo(job, driveRoot, rootBacking, bindfilter.Options{}); err != nil {
		t.Skipf("Bind Filter silo mappings unavailable (driver/elevation): %v", err)
	}
	defer bindfilter.RemoveSilo(job, driveRoot)

	mappedPaths := make([]string, 0, 4)
	defer func() {
		for index := len(mappedPaths) - 1; index >= 0; index-- {
			bindfilter.RemoveSilo(job, mappedPaths[index])
		}
	}()
	mapSamePath := func(path string) {
		if err := bindfilter.CreateSilo(job, path, path, bindfilter.Options{}); err != nil {
			t.Fatalf("create silo mapping %s: %v", path, err)
		}
		mappedPaths = append(mappedPaths, path)
	}
	mapSamePath(resolvedTarget)
	windowsDir := os.Getenv("SystemRoot")
	if windowsDir == "" {
		windowsDir = `C:\Windows`
	}
	mapSamePath(windowsDir)
	pwshPath, err := osExec.LookPath("pwsh.exe")
	if err != nil {
		t.Fatal(err)
	}
	mapSamePath(filepath.Dir(pwshPath))
	workingDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	mapSamePath(workingDir)

	probePath := filepath.Join(alias, name)
	probe := "if ((Get-Content -Raw -LiteralPath '" + strings.ReplaceAll(probePath, "'", "''") + "').Trim() -ne 'physical') { exit 1 }"
	exitCode, err := runInSilo(job, []string{pwshPath, "-NoProfile", "-Command", probe}, false, "")
	if shouldFallbackSiloLaunch(err) {
		exitCode, err = runInSiloFallback(job, []string{pwshPath, "-NoProfile", "-Command", probe}, false)
	}
	if err != nil {
		t.Fatalf("launch process in silo: %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("silo probe exited with code %d", exitCode)
	}
}

func TestSiloTempMappingsUseDedicatedDirectory(t *testing.T) {
	virtualTemp := filepath.Join(t.TempDir(), "temp")
	virtualTmp := filepath.Join(t.TempDir(), "tmp")
	for _, path := range []string{virtualTemp, virtualTmp} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	localAppData := t.TempDir()
	t.Setenv("TEMP", virtualTemp)
	t.Setenv("TMP", virtualTmp)
	t.Setenv("LOCALAPPDATA", localAppData)

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

	tempDir, err := prepareSiloTempDirectory(localAppData, "temp-integration")
	if err != nil {
		t.Fatal(err)
	}
	mapped := make(map[string]bool)
	if err := createTempMappings(job, tempDir, mapped, false, true); err != nil {
		t.Skipf("Bind Filter silo mappings unavailable (driver/elevation): %v", err)
	}
	defer bindfilter.RemoveSilo(job, virtualTmp)
	defer bindfilter.RemoveSilo(job, virtualTemp)

	mappings, err := bindfilter.ListSilo(job)
	if err != nil {
		t.Fatal(err)
	}
	assertTempMapping := func(virtualRoot string) {
		t.Helper()
		virtualInfo, err := os.Stat(virtualRoot)
		if err != nil {
			t.Fatal(err)
		}
		for _, mapping := range mappings {
			if len(mapping.Targets) != 1 {
				continue
			}
			mappingVirtualInfo, virtualErr := os.Stat(mapping.VirtualRoot)
			mappingTargetInfo, targetErr := os.Stat(mapping.Targets[0])
			if virtualErr == nil && targetErr == nil && os.SameFile(mappingVirtualInfo, virtualInfo) {
				tempInfo, err := os.Stat(tempDir)
				if err != nil {
					t.Fatal(err)
				}
				if os.SameFile(mappingTargetInfo, tempInfo) {
					return
				}
			}
		}
		t.Fatalf("mapping %q -> %q was not returned by ListSilo: %#v", virtualRoot, tempDir, mappings)
	}
	assertTempMapping(virtualTemp)
	assertTempMapping(virtualTmp)

	pwshPath, err := osExec.LookPath("pwsh.exe")
	if err != nil {
		t.Fatal(err)
	}
	probe := "Set-Content -NoNewline -LiteralPath (Join-Path $env:TEMP 'temp.txt') -Value 'temp'; " +
		"Set-Content -NoNewline -LiteralPath (Join-Path $env:TMP 'tmp.txt') -Value 'tmp'"
	exitCode, err := runInSilo(job, []string{pwshPath, "-NoProfile", "-Command", probe}, false, "")
	if shouldFallbackSiloLaunch(err) {
		exitCode, err = runInSiloFallback(job, []string{pwshPath, "-NoProfile", "-Command", probe}, false)
	}
	if err != nil {
		t.Fatalf("launch process in silo: %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("silo probe exited with code %d", exitCode)
	}

	for _, item := range []struct {
		name, contents string
	}{
		{"temp.txt", "temp"},
		{"tmp.txt", "tmp"},
	} {
		contents, err := os.ReadFile(filepath.Join(tempDir, item.name))
		if err != nil {
			t.Fatal(err)
		}
		if string(contents) != item.contents {
			t.Fatalf("%s contents = %q, want %q", item.name, contents, item.contents)
		}
	}
	for _, path := range []string{filepath.Join(virtualTemp, "temp.txt"), filepath.Join(virtualTmp, "tmp.txt")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("host temp path %q was unexpectedly written: %v", path, err)
		}
	}
}
