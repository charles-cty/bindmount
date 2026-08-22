//go:build windows

package main

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	osExec "os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"bindmount/internal/bindfilter"
	"bindmount/internal/winapi"
)

const execUsage = "bindmount exec [--detach] [--verbose] [--no-ui-restrictions] [--root data-dir | --readonly-root] [--passthrough name|--no-passthrough name]... [--link root[+][=|==]target]... <job-name> -- <command> [args...]"

func validPassthroughName(name string) bool {
	switch name {
	case "executable", "path", "cwd", "gitroot", "appstate", "appexec", "powershell":
		return true
	default:
		return false
	}
}

func shouldShowSkippedMappingWarnings(detach, verbose bool) bool {
	return !detach || verbose
}

// cmdExec implements: bindmount exec [--detach] [--root data-dir] [--link root[+][=|==]target]... <job-name> -- <command> [args...]
//
// It creates a job object, promotes it to a silo, optionally creates
// silo-scoped bind links, spawns the command inside the silo via
// PROC_THREAD_ATTRIBUTE_JOB_LIST, waits for it, and tears the job down when
// the command exits (JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE handles cleanup).
//
// Example:
//
//	bindmount exec --link C:\app\data=D:\shared\data --link C:\app\cfg==D:\cfg-ro mysilo -- cmd.exe /c dir C:\app\data
func cmdExec(args []string) error {
	return cmdExecInner(args)
}

func cmdExecInner(args []string) (err error) {
	// Split at "--": before it are our flags, after it the command.
	var ourArgs, cmdArgs []string
	for i, a := range args {
		if a == "--" {
			ourArgs = args[:i]
			cmdArgs = args[i+1:]
			break
		}
	}
	if cmdArgs == nil {
		return errors.New(`exec requires "--" before the child command (usage: ` + execUsage + ")")
	}
	detach := false
	noUIRestrictions := false
	rootDir := ""
	readOnlyRoot := false
	passthrough := map[string]bool{}
	passthroughSet := map[string]bool{}
	verbose := false
	filteredArgs := make([]string, 0, len(ourArgs))
	for i := 0; i < len(ourArgs); i++ {
		switch ourArgs[i] {
		case "--detach":
			detach = true
		case "--no-ui-restrictions":
			noUIRestrictions = true
		case "--root":
			if i+1 >= len(ourArgs) {
				return errors.New("--root requires a data directory")
			}
			rootDir = ourArgs[i+1]
			i++
		case "--readonly-root":
			readOnlyRoot = true
		case "--passthrough", "--no-passthrough":
			if i+1 >= len(ourArgs) || !validPassthroughName(ourArgs[i+1]) {
				return fmt.Errorf("%s requires one of: executable, path, cwd, gitroot, appstate, appexec, powershell", ourArgs[i])
			}
			name := ourArgs[i+1]
			i++
			passthrough[name] = ourArgs[i-1] == "--passthrough"
			passthroughSet[name] = true
		case "--verbose":
			verbose = true
		default:
			filteredArgs = append(filteredArgs, ourArgs[i])
		}
	}
	ourArgs = filteredArgs
	if len(ourArgs) < 1 {
		return errors.New("exec requires a job name")
	}

	jobName := ourArgs[len(ourArgs)-1]
	linkSpecs, err := parseLinkFlags(ourArgs[:len(ourArgs)-1])
	if err != nil {
		return err
	}
	if len(cmdArgs) == 0 {
		return errors.New("exec requires a command to run inside the silo")
	}
	if rootDir != "" && readOnlyRoot {
		return errors.New("--root and --readonly-root are mutually exclusive")
	}
	if rootDir != "" {
		for _, name := range []string{"executable", "path", "cwd", "gitroot", "appexec"} {
			if !passthroughSet[name] {
				passthrough[name] = true
			}
		}
	}
	showSkippedWarnings := shouldShowSkippedMappingWarnings(detach, verbose)
	executablePath := ""
	if passthrough["executable"] {
		executablePath, err = passthroughExecutable(cmdArgs[0])
		if err != nil {
			return fmt.Errorf("locate executable for passthrough %q: %w", cmdArgs[0], err)
		}
	}
	if existing, openErr := openSiloJob(jobName, winapi.JOB_OBJECT_ALL_ACCESS); openErr == nil {
		syscall.CloseHandle(existing)
		return fmt.Errorf("silo %q already exists", jobName)
	} else if !errors.Is(openErr, syscall.ERROR_FILE_NOT_FOUND) && !errors.Is(openErr, syscall.ERROR_PATH_NOT_FOUND) {
		return fmt.Errorf("check silo %q: %w", jobName, openErr)
	}

	job, err := winapi.CreateJob(jobName)
	if err != nil {
		return fmt.Errorf("create job %q: %w", jobName, err)
	}
	defer syscall.CloseHandle(job)
	if detach {
		if err := winapi.MakeHandleInheritable(job); err != nil {
			return fmt.Errorf("prepare detached silo handle: %w", err)
		}
	}

	// Job limits:
	//  - KILL_ON_JOB_CLOSE is required by JobObjectCreateSilo. Attached runs
	//    keep the handle here; detached runs pass an inheritable handle to the
	//    launched process so the silo survives bindmount exiting.
	//  - Both breakaway limits are deliberately absent: without
	//    JOB_OBJECT_LIMIT_BREAKAWAY_OK, a child created with
	//    CREATE_BREAKAWAY_FROM_JOB is denied breakaway, so the process tree
	//    cannot escape the silo (and its bind-link view). SILENT_BREAKAWAY_OK
	//    is also excluded by SetJobLimitFlags.
	if err := winapi.SetJobLimitFlags(job, winapi.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE); err != nil {
		return fmt.Errorf("configure job: %w", err)
	}
	if !noUIRestrictions {
		// Prevent silo processes from changing system/display settings, creating
		// desktops, or triggering system shutdown. Applications that create
		// nested jobs, including Chromium's Windows sandbox, require this to be
		// disabled because Windows does not allow UI limits in a job hierarchy.
		const siloUIRestrictions = winapi.JOB_OBJECT_UILIMIT_SYSTEMPARAMETERS |
			winapi.JOB_OBJECT_UILIMIT_DISPLAYSETTINGS |
			winapi.JOB_OBJECT_UILIMIT_DESKTOP |
			winapi.JOB_OBJECT_UILIMIT_EXITWINDOWS
		if err := winapi.SetJobUIRestrictions(job, siloUIRestrictions); err != nil {
			return fmt.Errorf("configure job UI restrictions: %w", err)
		}
	} else if verbose {
		fmt.Println("bindmount: job UI restrictions disabled")
	}
	if err := winapi.PromoteToSilo(job); err != nil {
		return fmt.Errorf("promote job to silo: %w", err)
	}
	mappedPassthrough := make(map[string]bool)
	if rootDir != "" {
		if err := createRootMappings(job, rootDir, mappedPassthrough, verbose, showSkippedWarnings); err != nil {
			return err
		}
	}
	if readOnlyRoot {
		if err := createReadOnlyRootMappings(job, mappedPassthrough, verbose, showSkippedWarnings); err != nil {
			return err
		}
	}
	if passthrough["powershell"] {
		tempDir, err := prepareSiloTempDirectory(os.Getenv("LOCALAPPDATA"), jobName)
		if err != nil {
			return fmt.Errorf("prepare silo temp directory: %w", err)
		}
		if err := createTempMappings(job, tempDir, mappedPassthrough, verbose, showSkippedWarnings); err != nil {
			return err
		}
	}
	if passthrough["path"] {
		if err := createPathMappings(job, rootDir, mappedPassthrough, verbose, showSkippedWarnings); err != nil {
			return err
		}
	}
	if passthrough["cwd"] {
		workingDir, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("get current working directory: %w", err)
		}
		if err := createWorkingDirectoryMapping(job, workingDir, mappedPassthrough, verbose, showSkippedWarnings); err != nil {
			return err
		}
	}
	if passthrough["gitroot"] {
		if err := createGitRootMapping(job, mappedPassthrough, verbose, showSkippedWarnings); err != nil {
			return err
		}
	}
	if passthrough["appstate"] {
		if err := createAppStateMappings(job, mappedPassthrough, verbose, showSkippedWarnings); err != nil {
			return err
		}
	}
	if passthrough["appexec"] {
		if err := createAppExecMappings(job, mappedPassthrough, verbose, showSkippedWarnings); err != nil {
			return err
		}
	}
	if passthrough["executable"] {
		if err := createExecutableMapping(job, executablePath, mappedPassthrough, verbose, showSkippedWarnings); err != nil {
			return err
		}
	}

	// Optional mapping: PowerShell profile/history for script trust.
	// Enabled with --passthrough powershell.
	if passthrough["powershell"] {
		if err := createPowerShellMappings(job, mappedPassthrough, verbose, showSkippedWarnings); err != nil {
			return err
		}
	}

	// Create any requested silo-scoped links before launching the process.
	for _, l := range linkSpecs {
		if err := createSiloLink(job, l, mappedPassthrough, verbose, showSkippedWarnings); err != nil {
			return err
		}
	}
	exitCode, err := runInSilo(job, cmdArgs, detach, "")
	// Package identity is intentionally not supplied: combining
	// PROC_THREAD_ATTRIBUTE_PACKAGE_FULL_NAME with a silo job in the attribute
	// list is rejected on build 26100. The fallback path likewise launches
	// without a package identity.
	if shouldFallbackSiloLaunch(err) {
		// If the JOB_LIST launch path is unavailable (observed on build 26100
		// as CreateProcess returning ERROR_INVALID_PARAMETER for a silo job),
		// create the process suspended and assign it to the job instead.
		exitCode, err = runInSiloFallback(job, cmdArgs, detach)
	}
	if err != nil {
		return err
	}
	if !detach && exitCode != 0 {
		exitWith(exitCode)
	}
	return nil
}

func passthroughExecutable(command string) (string, error) {
	path, err := osExec.LookPath(command)
	if err != nil {
		return "", err
	}
	return filepath.Clean(path), nil
}

func warnSkippedMapping(enabled bool, kind, root, target, reason string) {
	if !enabled {
		return
	}
	if target == "" {
		fmt.Fprintf(os.Stderr, "bindmount: warning: skipping %s mapping %s: %s\n", kind, root, reason)
		return
	}
	fmt.Fprintf(os.Stderr, "bindmount: warning: skipping %s mapping %s -> %s: %s\n", kind, root, target, reason)
}

func createPassthroughMapping(job syscall.Handle, name, path string, mapped map[string]bool, verbose, showSkippedWarnings bool) error {
	return createPassthroughMappingTo(job, name, path, path, mapped, verbose, showSkippedWarnings)
}

func createPassthroughMappingTo(job syscall.Handle, name, virtualRoot, target string, mapped map[string]bool, verbose, showSkippedWarnings bool) error {
	virtualRoot = filepath.Clean(virtualRoot)
	target = filepath.Clean(target)
	if virtualRoot == "." || virtualRoot == "" {
		return nil
	}
	volume := filepath.VolumeName(virtualRoot)
	if volume != "" && virtualRoot == filepath.Clean(volume+string(filepath.Separator)) {
		return nil
	}
	key := strings.ToLower(virtualRoot)
	if mapped[key] {
		warnSkippedMapping(showSkippedWarnings, name+" passthrough", virtualRoot, target, "virtual root is already mapped")
		return nil
	}
	if err := bindfilter.CreateSilo(job, virtualRoot, target, bindfilter.Options{}); err != nil {
		return fmt.Errorf("create %s passthrough %s -> %s: %w", name, virtualRoot, target, err)
	}
	if verbose {
		fmt.Printf("bindmount: passthrough %s %s -> %s\n", name, virtualRoot, target)
	}
	mapped[key] = true
	return nil
}

func createPathMappings(job syscall.Handle, rootDir string, mapped map[string]bool, verbose, showSkippedWarnings bool) error {
	seen := make(map[string]bool)
	for _, entry := range strings.Split(os.Getenv("PATH"), string(os.PathListSeparator)) {
		path := filepath.Clean(strings.Trim(entry, `"`))
		if path == "." || path == "" || seen[strings.ToLower(path)] {
			continue
		}
		seen[strings.ToLower(path)] = true
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			continue
		}
		if rootDir != "" {
			linkTarget, resolvedTarget, isLink, err := directorySymbolicLink(path)
			if err != nil {
				return fmt.Errorf("inspect path passthrough %s: %w", path, err)
			}
			if isLink {
				if _, err := stageDirectorySymbolicLink(rootDir, path, linkTarget); err != nil {
					return fmt.Errorf("stage path passthrough link %s: %w", path, err)
				}
				if err := createPassthroughMapping(job, "path", resolvedTarget, mapped, verbose, showSkippedWarnings); err != nil {
					return err
				}
				mapped[strings.ToLower(path)] = true
				if verbose {
					fmt.Printf("bindmount: staged path link %s -> %s\n", path, resolvedTarget)
				}
				continue
			}
		}
		if err := createPassthroughMapping(job, "path", path, mapped, verbose, showSkippedWarnings); err != nil {
			return err
		}
	}
	return nil
}

func directorySymbolicLink(path string) (linkTarget, resolvedTarget string, isLink bool, err error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", "", false, err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return "", "", false, nil
	}
	linkTarget, err = os.Readlink(path)
	if err != nil {
		return "", "", false, err
	}
	resolvedTarget, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", "", false, err
	}
	return linkTarget, filepath.Clean(resolvedTarget), true, nil
}

func rootBackingPath(rootDir, virtualPath string) (string, error) {
	virtualPath = filepath.Clean(virtualPath)
	volume := filepath.VolumeName(virtualPath)
	if len(volume) != 2 || volume[1] != ':' {
		return "", fmt.Errorf("%q is not on a drive-letter volume", virtualPath)
	}
	relative := strings.TrimLeft(virtualPath[len(volume):], `\`)
	if relative == "" {
		return "", fmt.Errorf("%q is a drive root", virtualPath)
	}
	return filepath.Join(rootDir, strings.ToUpper(volume[:1]), relative), nil
}

func stageDirectorySymbolicLink(rootDir, virtualPath, linkTarget string) (string, error) {
	backingPath, err := rootBackingPath(rootDir, virtualPath)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(backingPath), 0o755); err != nil {
		return "", err
	}
	if existing, err := os.Lstat(backingPath); err == nil {
		if existing.Mode()&os.ModeSymlink == 0 {
			return "", fmt.Errorf("%s already exists and is not a symbolic link", backingPath)
		}
		existingTarget, err := os.Readlink(backingPath)
		if err != nil {
			return "", err
		}
		if strings.EqualFold(existingTarget, linkTarget) {
			return backingPath, nil
		}
		if err := os.Remove(backingPath); err != nil {
			return "", err
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := os.Symlink(linkTarget, backingPath); err != nil {
		return "", err
	}
	return backingPath, nil
}

func createAppStateMappings(job syscall.Handle, mapped map[string]bool, verbose, showSkippedWarnings bool) error {
	seen := make(map[string]bool)
	for _, item := range []struct{ name, value string }{
		{"appdata", os.Getenv("APPDATA")},
		{"localappdata", os.Getenv("LOCALAPPDATA")},
		{"programdata", os.Getenv("ProgramData")},
	} {
		path := filepath.Clean(item.value)
		key := strings.ToLower(path)
		if path == "." || path == "" || seen[key] {
			continue
		}
		seen[key] = true
		if err := createPassthroughMapping(job, item.name, path, mapped, verbose, showSkippedWarnings); err != nil {
			return err
		}
	}
	return nil
}

// createPowerShellMappings installs two always-on bind links that make
// PowerShell usable inside the silo without write access to the rest of the
// user profile:
//
//  1. The PSReadLine history file (file-level link) so command history persists
//     across silo sessions.
//
//  2. %LOCALAPPDATA%\Microsoft\PowerShell (directory link) for PowerShell's
//     per-user module cache, telemetry opt-out, and related state.
//
// Both paths are silently skipped when the environment variable is absent or
// the path does not exist, keeping the function safe to call unconditionally.
type namedMappingPath struct {
	name, path string
}

func powerShellMappingPaths(appdata, localAppData string) []namedMappingPath {
	items := make([]namedMappingPath, 0, 2)
	if appdata != "" {
		items = append(items, namedMappingPath{
			name: "powershell-history",
			path: filepath.Clean(filepath.Join(appdata, `Microsoft\Windows\PowerShell\PSReadLine\ConsoleHost_history.txt`)),
		})
	}
	if localAppData != "" {
		items = append(items, namedMappingPath{
			name: "powershell-local",
			path: filepath.Clean(filepath.Join(localAppData, `Microsoft\PowerShell`)),
		})
	}
	return items
}

func createPowerShellMappings(job syscall.Handle, mapped map[string]bool, verbose, showSkippedWarnings bool) error {
	for _, item := range powerShellMappingPaths(os.Getenv("APPDATA"), os.Getenv("LOCALAPPDATA")) {
		if _, err := os.Stat(item.path); err != nil {
			// Path does not exist yet — skip silently. PSReadLine creates the
			// history file on first use; the local cache dir is created by
			// PowerShell on first launch.
			continue
		}
		if err := createPassthroughMapping(job, item.name, item.path, mapped, verbose, showSkippedWarnings); err != nil {
			return err
		}
	}
	return nil
}

func siloTempDirectory(localAppData, jobName string) (string, error) {
	localAppData = filepath.Clean(localAppData)
	if localAppData == "." || localAppData == "" || !filepath.IsAbs(localAppData) {
		return "", errors.New("LOCALAPPDATA must be an absolute path")
	}
	jobHash := sha256.Sum256([]byte(jobName))
	return filepath.Join(localAppData, "bindmount", "tempdirs", fmt.Sprintf("%x", jobHash)), nil
}

// prepareSiloTempDirectory clears the target used by a newly created silo.
// Its deterministic, job-name-derived path lets a later launch remove stale
// files left after a previous silo with the same name exited.
func prepareSiloTempDirectory(localAppData, jobName string) (string, error) {
	tempDir, err := siloTempDirectory(localAppData, jobName)
	if err != nil {
		return "", err
	}
	if err := os.RemoveAll(tempDir); err != nil {
		return "", err
	}
	if err := os.MkdirAll(tempDir, 0o755); err != nil {
		return "", err
	}
	return tempDir, nil
}

func tempMappingPaths(temp, tmp string) []string {
	seen := make(map[string]bool)
	paths := make([]string, 0, 2)
	for _, path := range []string{temp, tmp} {
		path = filepath.Clean(path)
		if path == "." || path == "" || seen[strings.ToLower(path)] {
			continue
		}
		seen[strings.ToLower(path)] = true
		paths = append(paths, path)
	}
	return paths
}

// createTempMappings maps both %TEMP% and %TMP%, where defined, to the
// silo-specific directory prepared under %LOCALAPPDATA%\bindmount\tempdirs.
// WLDP's developer-mode script trust writes a record there when evaluating
// PowerShell scripts; without the writable mapping, PowerShell can enter
// Constrained Language Mode. This mapping is installed before PATH mappings so
// a coincidental TEMP entry in PATH cannot retain a host-backed mapping.
func createTempMappings(job syscall.Handle, tempDir string, mapped map[string]bool, verbose, showSkippedWarnings bool) error {
	for _, virtualRoot := range tempMappingPaths(os.Getenv("TEMP"), os.Getenv("TMP")) {
		if err := createPassthroughMappingTo(job, "temp", virtualRoot, tempDir, mapped, verbose, showSkippedWarnings); err != nil {
			return err
		}
	}
	return nil
}

// createAppExecMappings resolves every App Execution Alias (.exe reparse
// point) under %LOCALAPPDATA%\Microsoft\WindowsApps and installs two bind
// links for each one inside the silo:
//
//  1. A directory passthrough for the folder that contains the real binary,
//     ensuring the executable's sibling DLLs and data files are visible to
//     the activation path inside the silo.
//
//  2. A directory passthrough for the package's per-user state folder under
//     %LOCALAPPDATA%\Packages\<family>, so a packaged app can reach its state
//     store (winget fails without it with 0x80073db8).
//
// The WindowsApps directory itself is silently skipped if it cannot be read
// or if none of its entries carry an APPEXECLINK reparse tag; this keeps
// appexec opt-in and non-fatal.
func createAppExecMappings(job syscall.Handle, mapped map[string]bool, verbose, showSkippedWarnings bool) error {
	localAppData := os.Getenv("LOCALAPPDATA")
	if localAppData == "" {
		return nil
	}
	windowsAppsDir := filepath.Join(localAppData, "Microsoft", "WindowsApps")

	entries, err := os.ReadDir(windowsAppsDir)
	if err != nil {
		// WindowsApps is absent or inaccessible — not an error for the caller.
		return nil
	}

	// Passthrough the alias directory itself so PATH-based lookup can reach
	// the individual alias files inside it even when --root has shadowed the
	// drive with an empty backing tree.
	if err := createPassthroughMapping(job, "appexec", windowsAppsDir, mapped, verbose, showSkippedWarnings); err != nil {
		return err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if !strings.EqualFold(filepath.Ext(entry.Name()), ".exe") {
			continue
		}
		aliasPath := filepath.Join(windowsAppsDir, entry.Name())

		info, err := winapi.ReadAppExecLinkInfo(aliasPath)
		if err != nil || info == nil || info.ExePath == "" {
			// Not an APPEXECLINK or data unreadable — skip silently.
			continue
		}
		realExe := filepath.Clean(info.ExePath)

		// Directory passthrough for the real binary's package folder. The
		// App Model activation path dereferences the APPEXECLINK's embedded
		// executable path using the silo's own view, so the package folder
		// must be visible inside the silo; under --root that drive has been
		// shadowed by an empty backing tree. Best-effort: an alias whose
		// package folder was removed by an app update (stale reparse data)
		// is skipped rather than aborting the whole appexec pass.
		if err := createPassthroughMapping(job, "appexec", filepath.Dir(realExe), mapped, verbose, showSkippedWarnings); err != nil {
			if verbose {
				fmt.Printf("bindmount: appexec package %s: %v (skipped)\n", filepath.Dir(realExe), err)
			}
			continue
		}

		// Per-user package state folder passthrough. Packaged apps keep
		// their writable state under %LOCALAPPDATA%\Packages\<family>; without
		// it winget fails at startup with 0x80073db8 (state store load). The
		// family name is string[0] of the reparse payload, already decoded above.
		if info.PackageFamilyName != "" {
			stateDir := filepath.Join(localAppData, "Packages", info.PackageFamilyName)
			if stat, err := os.Stat(stateDir); err == nil && stat.IsDir() {
				if err := createPassthroughMapping(job, "appexec", stateDir, mapped, verbose, showSkippedWarnings); err != nil {
					if verbose {
						fmt.Printf("bindmount: appexec state %s: %v (skipped)\n", stateDir, err)
					}
				}
			}
		}

	}
	return nil
}

func createGitRootMapping(job syscall.Handle, mapped map[string]bool, verbose, showSkippedWarnings bool) error {
	workingDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get current working directory for gitroot passthrough: %w", err)
	}
	command := osExec.Command("git", "rev-parse", "--show-toplevel")
	command.Dir = workingDir
	output, err := command.Output()
	if err != nil {
		// Git is optional. A missing Git executable or a non-repository cwd
		// simply means there is no gitroot passthrough to install.
		return nil
	}
	root := strings.TrimSpace(string(output))
	if root == "" {
		return nil
	}
	return createPassthroughMapping(job, "gitroot", root, mapped, verbose, showSkippedWarnings)
}

func createExecutableMapping(job syscall.Handle, executablePath string, mapped map[string]bool, verbose, showSkippedWarnings bool) error {
	virtualRoot := filepath.Dir(executablePath)
	return createPassthroughMapping(job, "executable", virtualRoot, mapped, verbose, showSkippedWarnings)
}

func pathRelativeForDrive(path string, letter rune) (string, bool) {
	if path == "" {
		return "", false
	}
	cleaned := filepath.Clean(path)
	if !filepath.IsAbs(cleaned) {
		return "", false
	}
	volume := filepath.VolumeName(cleaned)
	if !strings.EqualFold(volume, fmt.Sprintf("%c:", letter)) {
		return "", false
	}
	relative := strings.TrimLeft(cleaned[len(volume):], `\`)
	return relative, relative != ""
}

func profileRelativeForDrive(profile string, letter rune) (string, bool) {
	return pathRelativeForDrive(profile, letter)
}

// rootInitializationPaths returns the host paths whose namespace anchors are
// useful inside a --root silo. These are deliberately created only in the
// backing tree; they are not passthrough mappings and therefore remain empty
// until the caller installs an explicit mapping or creates content there.
func rootInitializationPaths() []string {
	paths := make([]string, 0, len(rootInitializationEnvironmentVariables))
	for _, name := range rootInitializationEnvironmentVariables {
		if value := os.Getenv(name); value != "" {
			paths = append(paths, value)
		}
	}
	return paths
}

var rootInitializationEnvironmentVariables = []string{
	"USERPROFILE",
	"PUBLIC",
	"APPDATA",
	"LOCALAPPDATA",
	"ProgramFiles",
	"ProgramFiles(x86)",
	"ProgramW6432",
	"ProgramData",
	"CommonProgramFiles",
	"CommonProgramFiles(x86)",
	"CommonProgramW6432",
	"TEMP",
	"TMP",
	"SystemRoot",
	"WINDIR",
}

func rootRelativePathsForDrive(paths []string, letter rune) []string {
	relatives := make([]string, 0, len(paths))
	seen := make(map[string]bool)
	for _, path := range paths {
		relative, ok := pathRelativeForDrive(path, letter)
		if !ok {
			continue
		}
		key := strings.ToLower(relative)
		if seen[key] {
			continue
		}
		seen[key] = true
		relatives = append(relatives, relative)
	}
	return relatives
}

func createRootMappings(job syscall.Handle, dataDir string, mapped map[string]bool, verbose, showSkippedWarnings bool) error {
	if dataDir == "" {
		return errors.New("root data directory is required")
	}
	drives, err := winapi.LogicalDriveLetters()
	if err != nil {
		return fmt.Errorf("enumerate drives for root mappings: %w", err)
	}
	initializationPaths := rootInitializationPaths()
	for _, letter := range drives {
		root := fmt.Sprintf("%c:\\", letter)
		target := filepath.Join(dataDir, string(letter))
		if err := os.MkdirAll(target, 0o755); err != nil {
			return fmt.Errorf("create root backing %s: %w", target, err)
		}
		// Pre-create common namespace anchors in the drive's backing tree without
		// installing corresponding bind links. This keeps paths such as
		// %APPDATA% and %ProgramFiles% resolvable while leaving them empty.
		for _, relative := range rootRelativePathsForDrive(initializationPaths, letter) {
			if err := os.MkdirAll(filepath.Join(target, relative), 0o755); err != nil {
				return fmt.Errorf("create root backing %s: %w", relative, err)
			}
		}
		if mapped[strings.ToLower(root)] {
			warnSkippedMapping(showSkippedWarnings, "root", root, target, "virtual root is already mapped")
			continue
		}
		if err := bindfilter.CreateSilo(job, root, target, bindfilter.Options{}); err != nil {
			return fmt.Errorf("create root mapping %s -> %s: %w", root, target, err)
		}
		mapped[strings.ToLower(root)] = true
		if verbose {
			fmt.Printf("bindmount: mapping drive %s -> %s\n", root, target)
		}
	}
	return nil
}

// createReadOnlyRootMappings maps every drive currently visible to the caller
// onto itself, read-only. Unlike --root there is no backing tree: each drive
// keeps its real contents inside the silo but rejects writes. Mutually
// exclusive with --root.
func createReadOnlyRootMappings(job syscall.Handle, mapped map[string]bool, verbose, showSkippedWarnings bool) error {
	drives, err := winapi.LogicalDriveLetters()
	if err != nil {
		return fmt.Errorf("enumerate drives for read-only root mappings: %w", err)
	}
	for _, letter := range drives {
		root := fmt.Sprintf("%c:\\", letter)
		if mapped[strings.ToLower(root)] {
			warnSkippedMapping(showSkippedWarnings, "read-only root", root, root, "virtual root is already mapped")
			continue
		}
		if err := bindfilter.CreateSilo(job, root, root, bindfilter.Options{ReadOnly: true}); err != nil {
			return fmt.Errorf("create read-only root mapping %s -> %s: %w", root, root, err)
		}
		mapped[strings.ToLower(root)] = true
		if verbose {
			fmt.Printf("bindmount: mapping drive %s -> %s (read-only)\n", root, root)
		}
	}
	return nil
}

// createWorkingDirectoryMapping exposes the launcher's current working
// directory inside the silo. This supplements --root mode: the cwd is
// already reachable when it lies under a drive that has been passed through,
// but is installed as an explicit narrow mapping when --root has shadowed
// that drive with an empty backing tree. A drive root needs no narrower
// mapping because it is already the root mapping.
func createWorkingDirectoryMapping(job syscall.Handle, workingDir string, mapped map[string]bool, verbose, showSkippedWarnings bool) error {
	workingDir = filepath.Clean(workingDir)
	return createPassthroughMapping(job, "cwd", workingDir, mapped, verbose, showSkippedWarnings)
}

type linkSpec struct {
	root, target     string
	readOnly, merged bool
}

// parseLinkFlags scans args for --link/-l mapping specifications. A plus
// before the separator marks a merged mapping; a double equals separator
// marks a read-only mapping.
func parseLinkFlags(args []string) ([]linkSpec, error) {
	var specs []linkSpec
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--link", "-l":
			i++
			if i >= len(args) {
				return nil, errors.New("--link requires root=target")
			}
			root, target, readOnly, merged, ok := splitLinkSpec(args[i])
			if !ok {
				return nil, fmt.Errorf("invalid --link %q: want root[+][=|==]target", args[i])
			}
			specs = append(specs, linkSpec{root: root, target: target, readOnly: readOnly, merged: merged})
		default:
			return nil, fmt.Errorf("unknown exec flag %q", args[i])
		}
	}
	return specs, nil
}

// splitLinkSpec parses a single mapping specification of the form
// root[+][=|==]target.
//
//   - A bare "=" separator creates a writable (default) mapping.
//   - A "==" separator creates a read-only mapping.
//   - A "+" immediately before the separator enables merged mode.
//
// Limitation: a literal "+" at the end of the root path is indistinguishable
// from the merged-mode marker. Paths containing a trailing "+" must be
// supplied as two separate arguments to the "add" command instead.
func splitLinkSpec(s string) (root, target string, readOnly, merged, ok bool) {
	separatorIndex := strings.IndexByte(s, '=')
	if separatorIndex <= 0 {
		return "", "", false, false, false
	}
	root = s[:separatorIndex]
	if strings.HasSuffix(root, "+") {
		merged = true
		root = strings.TrimSuffix(root, "+")
	}
	separator := "="
	if separatorIndex+1 < len(s) && s[separatorIndex+1] == '=' {
		separator = "=="
		readOnly = true
	}
	if root == "" || separatorIndex+len(separator) >= len(s) {
		return "", "", false, false, false
	}
	return root, s[separatorIndex+len(separator):], readOnly, merged, true
}

func createSiloLink(job syscall.Handle, l linkSpec, mapped map[string]bool, verbose, showSkippedWarnings bool) error {
	key := strings.ToLower(filepath.Clean(l.root))
	if mapped[key] {
		warnSkippedMapping(showSkippedWarnings, "user", l.root, l.target, "virtual root is already mapped")
		return nil
	}
	// Anchoring a mapping on an app execution alias (0-byte APPEXECLINK
	// reparse point, e.g. the wsl.exe alias under WindowsApps) fails with
	// "The file cannot be accessed by the system", so skip that link.
	info, err := os.Lstat(l.root)
	if err == nil && info.Size() == 0 && !info.IsDir() {
		if _, err := winapi.ReadAppExecLinkInfo(l.root); err == nil {
			warnSkippedMapping(showSkippedWarnings, "user", l.root, l.target, "app execution aliases cannot be mapped")
			mapped[key] = true
			return nil
		}
	}
	opts := bindfilter.Options{ReadOnly: l.readOnly, Merged: l.merged}
	err2 := bindfilter.CreateSilo(job, l.root, l.target, opts)
	if err2 != nil {
		return fmt.Errorf("create silo link %s -> %s: %w", l.root, l.target, err2)
	}
	if verbose {
		fmt.Printf("bindmount: mapping user %s -> %s\n", l.root, l.target)
	}
	mapped[key] = true
	// Mapping setup is intentionally quiet for exec: the launched command is
	// the user-facing process, and its console should not be prefixed by
	// supervisor diagnostics.
	return nil
}
