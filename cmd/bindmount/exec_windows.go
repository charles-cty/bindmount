//go:build windows

package main

import (
	"errors"
	"fmt"
	"syscall"

	"bindmount/internal/bindfilter"
	"bindmount/internal/winapi"
)

// cmdExec implements: bindmount exec [--link root=target [--read-only] [--merged]]... <job-name> -- <command> [args...]
//
// It creates a job object, promotes it to a silo, optionally creates
// silo-scoped bind links, spawns the command inside the silo via
// PROC_THREAD_ATTRIBUTE_JOB_LIST, waits for it, and tears the job down when
// the command exits (JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE handles cleanup).
//
// Example:
//
//	bindmount exec --link C:\app\data=D:\shared\data --read-only mysilo -- cmd.exe /c dir C:\app\data
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
			return errors.New("usage: bindmount exec [--link root=target [--read-only] [--merged]] <job-name> -- <command> [args...]")
		}
		ourArgs = args[:1]
		cmdArgs = args[1:]
	}
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

	job, err := winapi.CreateJob(jobName)
	if err != nil {
		return fmt.Errorf("create job %q: %w", jobName, err)
	}
	defer syscall.CloseHandle(job)

	if err := winapi.SetKillOnJobClose(job); err != nil {
		return fmt.Errorf("configure job: %w", err)
	}
	if err := winapi.PromoteToSilo(job); err != nil {
		return fmt.Errorf("promote job to silo: %w", err)
	}

	// Create any requested silo-scoped links before launching the process.
	for _, l := range linkSpecs {
		if err := createSiloLink(job, l); err != nil {
			return err
		}
	}

	exitCode, err := runInSilo(job, cmdArgs)
	if err != nil {
		// If CreateProcess with PROC_THREAD_ATTRIBUTE_JOB_LIST fails (observed
		// on build 26100 with ERROR_INVALID_PARAMETER for a silo job), fall
		// back to creating the process suspended and assigning it to the job.
		exitCode, err = runInSiloFallback(job, cmdArgs)
		if err != nil {
			return err
		}
	}
	// Propagate the child's exit code directly rather than wrapping it as an
	// error, so `bindmount exec` is transparent in scripts.
	if exitCode != 0 {
		exitWith(exitCode)
	}
	return nil
}

type linkSpec struct {
	root, target     string
	readOnly, merged bool
}

// parseLinkFlags scans args for --link/-l root=target and the modifiers
// --read-only/--merged that apply to the most recent --link.
func parseLinkFlags(args []string) ([]linkSpec, error) {
	var specs []linkSpec
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--link", "-l":
			i++
			if i >= len(args) {
				return nil, errors.New("--link requires root=target")
			}
			root, target, ok := splitLinkSpec(args[i])
			if !ok {
				return nil, fmt.Errorf("invalid --link %q: want root=target", args[i])
			}
			specs = append(specs, linkSpec{root: root, target: target})
		case "--read-only":
			if len(specs) == 0 {
				return nil, errors.New("--read-only must follow a --link")
			}
			specs[len(specs)-1].readOnly = true
		case "--merged":
			if len(specs) == 0 {
				return nil, errors.New("--merged must follow a --link")
			}
			specs[len(specs)-1].merged = true
		default:
			return nil, fmt.Errorf("unknown exec flag %q", args[i])
		}
	}
	return specs, nil
}

func splitLinkSpec(s string) (root, target string, ok bool) {
	for i := 0; i < len(s); i++ {
		if s[i] == '=' {
			return s[:i], s[i+1:], i > 0 && i < len(s)-1
		}
	}
	return "", "", false
}

func createSiloLink(job syscall.Handle, l linkSpec) error {
	opts := bindfilter.Options{ReadOnly: l.readOnly, Merged: l.merged}
	err := bindfilter.CreateSilo(job, l.root, l.target, opts)
	if err != nil {
		return fmt.Errorf("create silo link %s -> %s: %w", l.root, l.target, err)
	}
	fmt.Printf("created silo mapping %s -> %s\n", l.root, l.target)
	return nil
}
