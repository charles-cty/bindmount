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
	case "executable", "path", "cwd", "gitroot", "appstate":
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
		for _, name := range []string{"executable", "path", "cwd", "gitroot"} {
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

	exitCode, err := runInSilo(job, cmdArgs, detach)
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
