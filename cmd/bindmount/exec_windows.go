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

const execUsage = "bindmount exec [--detach] [--verbose] [--root data-dir] [--passthrough executable|--no-passthrough executable] [--link root[+][=|==]target] <job-name> -- <command> [args...]"

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
	passthroughExecutableFlag := false
	passthroughExecutableSet := false
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
			if i+1 >= len(ourArgs) || ourArgs[i+1] != "executable" {
				return fmt.Errorf("%s requires the passthrough name executable", ourArgs[i])
			}
			i++
			passthroughExecutableFlag = true
			if ourArgs[i-1] == "--no-passthrough" {
				passthroughExecutableFlag = false
			}
			passthroughExecutableSet = true
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
	if rootDir != "" && !passthroughExecutableSet {
		passthroughExecutableFlag = true
	}
	executablePath := ""
	if passthroughExecutableFlag {
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
	if rootDir != "" {
		if err := createRootMappings(job, rootDir, verbose); err != nil {
			return err
		}
		workingDir, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("get current working directory: %w", err)
		}
		if err := createWorkingDirectoryMapping(job, workingDir, verbose); err != nil {
			return err
		}
	}
	if passthroughExecutableFlag {
		if err := createExecutableMapping(job, executablePath, verbose); err != nil {
			return err
		}
	}

	// Create any requested silo-scoped links before launching the process.
	for _, l := range linkSpecs {
		if err := createSiloLink(job, l, verbose); err != nil {
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

func createExecutableMapping(job syscall.Handle, executablePath string, verbose bool) error {
	virtualRoot := filepath.Dir(executablePath)
	volume := filepath.VolumeName(virtualRoot)
	if volume != "" && filepath.Clean(virtualRoot) == filepath.Clean(volume+string(filepath.Separator)) {
		return nil
	}
	if err := bindfilter.CreateSilo(job, virtualRoot, virtualRoot, bindfilter.Options{}); err != nil {
		return fmt.Errorf("create executable mapping %s -> %s: %w", virtualRoot, virtualRoot, err)
	}
	if verbose {
		fmt.Printf("bindmount: mapping executable %s -> %s\n", virtualRoot, virtualRoot)
	}
	return nil
}

func createRootMappings(job syscall.Handle, dataDir string, verbose bool) error {
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
		if err := bindfilter.CreateSilo(job, root, target, bindfilter.Options{}); err != nil {
			return fmt.Errorf("create root mapping %s -> %s: %w", root, target, err)
		}
		if verbose {
			fmt.Printf("bindmount: mapping drive %s -> %s\n", root, target)
		}
	}
	return nil
}

// createWorkingDirectoryMapping restores the caller's current directory
// after root mode shadows its drive with the portable backing tree. A drive
// root needs no narrower mapping because it is already the root mapping.
func createWorkingDirectoryMapping(job syscall.Handle, workingDir string, verbose bool) error {
	workingDir = filepath.Clean(workingDir)
	volume := filepath.VolumeName(workingDir)
	if volume != "" && workingDir == filepath.Clean(volume+string(filepath.Separator)) {
		return nil
	}
	if err := bindfilter.CreateSilo(job, workingDir, workingDir, bindfilter.Options{}); err != nil {
		return fmt.Errorf("create working-directory mapping %s -> %s: %w", workingDir, workingDir, err)
	}
	if verbose {
		fmt.Printf("bindmount: mapping working directory %s -> %s\n", workingDir, workingDir)
	}
	return nil
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

func createSiloLink(job syscall.Handle, l linkSpec, verbose bool) error {
	opts := bindfilter.Options{ReadOnly: l.readOnly, Merged: l.merged}
	err := bindfilter.CreateSilo(job, l.root, l.target, opts)
	if err != nil {
		return fmt.Errorf("create silo link %s -> %s: %w", l.root, l.target, err)
	}
	if verbose {
		fmt.Printf("bindmount: mapping user %s -> %s\n", l.root, l.target)
	}
	// Mapping setup is intentionally quiet for exec: the launched command is
	// the user-facing process, and its console should not be prefixed by
	// supervisor diagnostics.
	return nil
}
