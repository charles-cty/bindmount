//go:build windows

package main

import (
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

const execUsage = "bindmount exec [--detach] [--verbose] [--root data-dir] [--passthrough name|--no-passthrough name] [--link root[+][=|==]target] <job-name> -- <command> [args...]"

func validPassthroughName(name string) bool {
	switch name {
	case "executable", "path", "cwd", "gitroot", "appstate", "appexec":
		return true
	default:
		return false
	}
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
		// No "--" separator: first arg is the job name, rest is the command.
		if len(args) < 2 {
			return errors.New("usage: " + execUsage)
		}
		ourArgs = args[:1]
		cmdArgs = args[1:]
	}
	detach := false
	rootDir := ""
	passthrough := map[string]bool{}
	passthroughSet := map[string]bool{}
	verbose := false
	filteredArgs := make([]string, 0, len(ourArgs))
	for i := 0; i < len(ourArgs); i++ {
		switch ourArgs[i] {
		case "--detach":
			detach = true
		case "--root":
			if i+1 >= len(ourArgs) {
				return errors.New("--root requires a data directory")
			}
			rootDir = ourArgs[i+1]
			i++
		case "--passthrough", "--no-passthrough":
			if i+1 >= len(ourArgs) || !validPassthroughName(ourArgs[i+1]) {
				return fmt.Errorf("%s requires one of: executable, path, cwd, gitroot, appstate", ourArgs[i])
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
	if rootDir != "" {
		for _, name := range []string{"executable", "path", "cwd", "gitroot", "appexec"} {
			if !passthroughSet[name] {
				passthrough[name] = true
			}
		}
	}
	executablePath := ""
	if passthrough["executable"] {
		executablePath, err = passthroughExecutable(cmdArgs[0])
		if err != nil {
			return fmt.Errorf("locate executable for passthrough %q: %w", cmdArgs[0], err)
		}
	}
	if existing, openErr := winapi.OpenJob(jobName, winapi.JOB_OBJECT_ALL_ACCESS); openErr == nil {
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
	//  - KILL_ON_JOB_CLOSE tears the whole silo down when exec exits, so the
	//    command cannot outlive the tool.
	//  - Both breakaway limits are deliberately absent: without
	//    JOB_OBJECT_LIMIT_BREAKAWAY_OK, a child created with
	//    CREATE_BREAKAWAY_FROM_JOB is denied breakaway, so the process tree
	//    cannot escape the silo (and its bind-link view). SILENT_BREAKAWAY_OK
	//    is also excluded by SetJobLimitFlags.
	if err := winapi.SetJobLimitFlags(job, winapi.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE); err != nil {
		return fmt.Errorf("configure job: %w", err)
	}
	if err := winapi.PromoteToSilo(job); err != nil {
		return fmt.Errorf("promote job to silo: %w", err)
	}
	mappedPassthrough := make(map[string]bool)
	if rootDir != "" {
		if err := createRootMappings(job, rootDir, mappedPassthrough, verbose); err != nil {
			return err
		}
	}
	if passthrough["path"] {
		if err := createPathMappings(job, mappedPassthrough, verbose); err != nil {
			return err
		}
	}
	if passthrough["cwd"] {
		workingDir, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("get current working directory: %w", err)
		}
		if err := createWorkingDirectoryMapping(job, workingDir, mappedPassthrough, verbose); err != nil {
			return err
		}
	}
	if passthrough["gitroot"] {
		if err := createGitRootMapping(job, mappedPassthrough, verbose); err != nil {
			return err
		}
	}
	if passthrough["appstate"] {
		if err := createAppStateMappings(job, mappedPassthrough, verbose); err != nil {
			return err
		}
	}
	if passthrough["appexec"] {
		if err := createAppExecMappings(job, mappedPassthrough, verbose); err != nil {
			return err
		}
	}
	if passthrough["executable"] {
		if err := createExecutableMapping(job, executablePath, mappedPassthrough, verbose); err != nil {
			return err
		}
	}

	// Create any requested silo-scoped links before launching the process.
	for _, l := range linkSpecs {
		if err := createSiloLink(job, l, mappedPassthrough, verbose); err != nil {
			return err
		}
	}

	exitCode, err := runInSilo(job, cmdArgs, detach, "")
	if err != nil {
		// If CreateProcess with PROC_THREAD_ATTRIBUTE_JOB_LIST fails (observed
		// on build 26100 with ERROR_INVALID_PARAMETER for a silo job), fall
		// back to creating the process suspended and assigning it to the job.
		exitCode, err = runInSiloFallback(job, cmdArgs, detach)
		if err != nil {
			return err
		}
	}
	// Propagate the child's exit code directly rather than wrapping it as an
	// error, so `bindmount exec` is transparent in scripts.
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

func createPassthroughMapping(job syscall.Handle, name, path string, mapped map[string]bool, verbose bool) error {
	path = filepath.Clean(path)
	if path == "." || path == "" {
		return nil
	}
	volume := filepath.VolumeName(path)
	if volume != "" && path == filepath.Clean(volume+string(filepath.Separator)) {
		return nil
	}
	key := strings.ToLower(path)
	if mapped[key] {
		return nil
	}
	if err := bindfilter.CreateSilo(job, path, path, bindfilter.Options{}); err != nil {
		return fmt.Errorf("create %s passthrough %s -> %s: %w", name, path, path, err)
	}
	if verbose {
		fmt.Printf("bindmount: passthrough %s %s -> %s\n", name, path, path)
	}
	mapped[key] = true
	return nil
}

func createPathMappings(job syscall.Handle, mapped map[string]bool, verbose bool) error {
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
		if err := createPassthroughMapping(job, "path", path, mapped, verbose); err != nil {
			return err
		}
	}
	return nil
}

func createAppStateMappings(job syscall.Handle, mapped map[string]bool, verbose bool) error {
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
		if err := createPassthroughMapping(job, item.name, path, mapped, verbose); err != nil {
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
func createAppExecMappings(job syscall.Handle, mapped map[string]bool, verbose bool) error {
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
	if err := createPassthroughMapping(job, "appexec", windowsAppsDir, mapped, verbose); err != nil {
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

		realExe, err := winapi.ReadAppExecLink(aliasPath)
		if err != nil || realExe == "" {
			// Not an APPEXECLINK or data unreadable — skip silently.
			continue
		}
		realExe = filepath.Clean(realExe)

		// Directory passthrough for the real binary's package folder. The
		// App Model activation path dereferences the APPEXECLINK's embedded
		// executable path using the silo's own view, so the package folder
		// must be visible inside the silo; under --root that drive has been
		// shadowed by an empty backing tree. Best-effort: an alias whose
		// package folder was removed by an app update (stale reparse data)
		// is skipped rather than aborting the whole appexec pass.
		if err := createPassthroughMapping(job, "appexec", filepath.Dir(realExe), mapped, verbose); err != nil {
			if verbose {
				fmt.Printf("bindmount: appexec package %s: %v (skipped)\n", filepath.Dir(realExe), err)
			}
			continue
		}

		// Per-user package state folder passthrough. Packaged apps keep
		// their writable state under %LOCALAPPDATA%\Packages\<family>; without
		// it winget fails at startup with 0x80073db8 (state store load). The
		// family name is string[0] of the reparse payload, carried on info.
		if info, err := winapi.ReadAppExecLinkInfo(aliasPath); err == nil && info != nil && info.PackageFullName != "" {
			stateDir := filepath.Join(localAppData, "Packages", info.PackageFullName)
			if stat, err := os.Stat(stateDir); err == nil && stat.IsDir() {
				if err := createPassthroughMapping(job, "appexec", stateDir, mapped, verbose); err != nil {
					if verbose {
						fmt.Printf("bindmount: appexec state %s: %v (skipped)\n", stateDir, err)
					}
				}
			}
		}

	}
	return nil
}

func createGitRootMapping(job syscall.Handle, mapped map[string]bool, verbose bool) error {
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
	return createPassthroughMapping(job, "gitroot", root, mapped, verbose)
}

func createExecutableMapping(job syscall.Handle, executablePath string, mapped map[string]bool, verbose bool) error {
	virtualRoot := filepath.Dir(executablePath)
	return createPassthroughMapping(job, "executable", virtualRoot, mapped, verbose)
}

func createRootMappings(job syscall.Handle, dataDir string, mapped map[string]bool, verbose bool) error {
	if dataDir == "" {
		return errors.New("root data directory is required")
	}
	drives, err := winapi.LogicalDriveLetters()
	if err != nil {
		return fmt.Errorf("enumerate drives for root mappings: %w", err)
	}
	for _, letter := range drives {
		root := fmt.Sprintf("%c:\\", letter)
		target := filepath.Join(dataDir, string(letter))
		if err := os.MkdirAll(target, 0o755); err != nil {
			return fmt.Errorf("create root backing %s: %w", target, err)
		}
		// Pre-create the current profile's relative directory in the C: backing
		// tree without installing a corresponding bind link. This preserves the
		// normal profile directory and its NTFS short-name alias in root mode.
		if letter == 'C' {
			if profile := os.Getenv("USERPROFILE"); profile != "" {
				profileRelative := strings.TrimLeft(filepath.Clean(profile)[2:], `\`)
				if profileRelative != "" {
					if err := os.MkdirAll(filepath.Join(target, profileRelative), 0o755); err != nil {
						return fmt.Errorf("create profile backing %s: %w", profileRelative, err)
					}
				}
			}
		}
		if mapped[strings.ToLower(root)] {
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

// createWorkingDirectoryMapping restores the caller's current directory
// after root mode shadows its drive with the portable backing tree. A drive
// root needs no narrower mapping because it is already the root mapping.
func createWorkingDirectoryMapping(job syscall.Handle, workingDir string, mapped map[string]bool, verbose bool) error {
	workingDir = filepath.Clean(workingDir)
	return createPassthroughMapping(job, "cwd", workingDir, mapped, verbose)
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

// resolvePackageName looks up cmdArgs[0] in PATH and, if it resolves to an
// App Execution Alias under WindowsApps, returns its MSIX package full name
// for use with PROC_THREAD_ATTRIBUTE_PACKAGE_FULL_NAME. Returns empty string
// if the command is not a packaged app or the alias cannot be read.
func resolvePackageName(cmdArgs []string) string {
	if len(cmdArgs) == 0 {
		return ""
	}
	resolved, err := osExec.LookPath(cmdArgs[0])
	if err != nil {
		return ""
	}
	resolved = filepath.Clean(resolved)
	localAppData := os.Getenv("LOCALAPPDATA")
	if localAppData == "" {
		return ""
	}
	windowsAppsDir := strings.ToLower(filepath.Join(localAppData, "Microsoft", "WindowsApps"))
	if !strings.HasPrefix(strings.ToLower(resolved), windowsAppsDir) {
		return ""
	}
	info, err := winapi.ReadAppExecLinkInfo(resolved)
	if err != nil || info == nil {
		return ""
	}
	return info.PackageFullName
}

func createSiloLink(job syscall.Handle, l linkSpec, mapped map[string]bool, verbose bool) error {
	key := strings.ToLower(filepath.Clean(l.root))
	if mapped[key] {
		return nil
	}
	opts := bindfilter.Options{ReadOnly: l.readOnly, Merged: l.merged}
	err := bindfilter.CreateSilo(job, l.root, l.target, opts)
	if err != nil {
		return fmt.Errorf("create silo link %s -> %s: %w", l.root, l.target, err)
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
