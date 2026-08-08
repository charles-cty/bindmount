//go:build windows

package main

import (
	"errors"
	"fmt"
	"strings"
	"syscall"
	"unsafe"

	"bindmount/internal/winapi"
)

// runInSilo launches cmdArgs[0] inside the silo job using
// PROC_THREAD_ATTRIBUTE_JOB_LIST, waits for it, and returns its exit code.
func runInSilo(job syscall.Handle, cmdArgs []string) (uint32, error) {
	// Build the command line. CreateProcess requires a mutable buffer; the
	// quoting rule follows the CRT/CommandLineToArgvW convention.
	cmdLine := buildCommandLine(cmdArgs)

	var attrSize uintptr
	winapi.InitializeProcThreadAttributeList(nil, 1, 0, &attrSize)
	if attrSize == 0 {
		return 0, errors.New("InitializeProcThreadAttributeList reported zero size")
	}
	attrList := winapi.ProcThreadAttributeList(make([]byte, attrSize))
	if err := winapi.InitializeProcThreadAttributeList(attrList, 1, 0, &attrSize); err != nil {
		return 0, fmt.Errorf("InitializeProcThreadAttributeList: %w", err)
	}
	defer winapi.DeleteProcThreadAttributeList(attrList)

	jobHandle := job
	if err := winapi.UpdateProcThreadAttribute(
		attrList,
		0,
		winapi.PROC_THREAD_ATTRIBUTE_JOB_LIST,
		unsafe.Pointer(&jobHandle),
		unsafe.Sizeof(jobHandle),
	); err != nil {
		return 0, fmt.Errorf("UpdateProcThreadAttribute(JOB_LIST): %w", err)
	}

	var si winapi.StartupInfoEx
	si.StartupInfo.Cb = uint32(unsafe.Sizeof(si))
	si.AttributeList = attrList

	cmdPtr, err := syscall.UTF16PtrFromString(cmdLine)
	if err != nil {
		return 0, err
	}

	var pi syscall.ProcessInformation
	err = syscall.CreateProcess(
		nil,
		cmdPtr,
		nil,
		nil,
		false,
		winapi.EXTENDED_STARTUPINFO_PRESENT,
		nil,
		nil,
		&si.StartupInfo,
		&pi,
	)
	if err != nil {
		return 0, fmt.Errorf("CreateProcess(%q): %w", cmdLine, err)
	}
	defer syscall.CloseHandle(pi.Thread)
	defer syscall.CloseHandle(pi.Process)

	// The child inherits our console and stdio handles; no redirection is
	// wired up, which is what a bind-mount helper wants: run the command as
	// if the caller had launched it, just inside the silo.

	syscall.WaitForSingleObject(pi.Process, syscall.INFINITE)

	var exitCode uint32
	if err := syscall.GetExitCodeProcess(pi.Process, &exitCode); err != nil {
		return 0, fmt.Errorf("GetExitCodeProcess: %w", err)
	}
	return exitCode, nil
}

// buildCommandLine quotes arguments per CommandLineToArgvW rules.
func buildCommandLine(args []string) string {
	var b strings.Builder
	for i, a := range args {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(quoteArg(a))
	}
	return b.String()
}

func quoteArg(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n\v\"") {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	backslashes := 0
	for _, r := range s {
		if r == '\\' {
			backslashes++
			continue
		}
		if r == '"' {
			b.WriteString(strings.Repeat(`\`, backslashes*2+1))
			b.WriteByte('"')
			backslashes = 0
			continue
		}
		if backslashes > 0 {
			b.WriteString(strings.Repeat(`\`, backslashes))
			backslashes = 0
		}
		b.WriteRune(r)
	}
	if backslashes > 0 {
		b.WriteString(strings.Repeat(`\`, backslashes*2))
	}
	b.WriteByte('"')
	return b.String()
}

// runInSiloFallback creates the process suspended, assigns it to the job with
// AssignProcessToJobObject, then resumes it. Used when
// PROC_THREAD_ATTRIBUTE_JOB_LIST fails — observed on build 26100 where
// CreateProcess returns ERROR_INVALID_PARAMETER for an attribute list that
// references a silo job.
func runInSiloFallback(job syscall.Handle, cmdArgs []string) (uint32, error) {
	cmdLine := buildCommandLine(cmdArgs)
	cmdPtr, err := syscall.UTF16PtrFromString(cmdLine)
	if err != nil {
		return 0, err
	}

	var si syscall.StartupInfo
	si.Cb = uint32(unsafe.Sizeof(si))
	var pi syscall.ProcessInformation
	err = syscall.CreateProcess(
		nil,
		cmdPtr,
		nil,
		nil,
		false,
		winapi.CREATE_SUSPENDED,
		nil,
		nil,
		&si,
		&pi,
	)
	if err != nil {
		return 0, fmt.Errorf("CreateProcess suspended (%q): %w", cmdLine, err)
	}

	if err := winapi.AssignProcessToJob(job, pi.Process); err != nil {
		syscall.TerminateProcess(pi.Process, 1)
		syscall.CloseHandle(pi.Thread)
		syscall.CloseHandle(pi.Process)
		return 0, fmt.Errorf("assign process to job: %w", err)
	}

	if _, err := resumeThread(pi.Thread); err != nil {
		syscall.TerminateProcess(pi.Process, 1)
		syscall.CloseHandle(pi.Thread)
		syscall.CloseHandle(pi.Process)
		return 0, fmt.Errorf("resume process: %w", err)
	}

	defer syscall.CloseHandle(pi.Thread)
	defer syscall.CloseHandle(pi.Process)

	syscall.WaitForSingleObject(pi.Process, syscall.INFINITE)

	var exitCode uint32
	if err := syscall.GetExitCodeProcess(pi.Process, &exitCode); err != nil {
		return 0, fmt.Errorf("GetExitCodeProcess: %w", err)
	}
	return exitCode, nil
}

var procResumeThread = syscall.NewLazyDLL("kernel32.dll").NewProc("ResumeThread")

func resumeThread(thread syscall.Handle) (uint32, error) {
	r, _, err := procResumeThread.Call(uintptr(thread))
	if r == 0xFFFFFFFF {
		if err != syscall.Errno(0) {
			return 0, err
		}
		return 0, errors.New("ResumeThread failed")
	}
	return uint32(r), nil
}
