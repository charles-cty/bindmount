//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"unsafe"

	"bindmount/internal/winapi"
)

const startfUseStdHandles = 0x00000100

// Standard handle constants for GetStdHandle (public Win32 values).
const (
	stdInputHandle  = -10
	stdOutputHandle = -11
	stdErrorHandle  = -12
)

// waitFailed is the WAIT_FAILED sentinel returned by WaitForSingleObject on
// error; the syscall package does not export this constant.
const waitFailed = 0xFFFFFFFF

// errorInvalidParameter is the Win32 ERROR_INVALID_PARAMETER value returned
// when a silo job is rejected in PROC_THREAD_ATTRIBUTE_JOB_LIST.
const errorInvalidParameter syscall.Errno = 87

// siloFallbackError marks a primary launch failure for which no process was
// created and the suspended-create + AssignProcessToJob fallback is safe. It
// is deliberately limited to failures caused by the JOB_LIST launch path.
type siloFallbackError struct {
	err error
}

func (e *siloFallbackError) Error() string { return e.err.Error() }
func (e *siloFallbackError) Unwrap() error { return e.err }

func allowSiloFallback(err error) error {
	return &siloFallbackError{err: err}
}

func shouldFallbackSiloLaunch(err error) bool {
	var fallbackErr *siloFallbackError
	return errors.As(err, &fallbackErr)
}

func inheritStandardHandles(si *syscall.StartupInfo) {
	in, inErr := syscall.GetStdHandle(stdInputHandle)
	out, outErr := syscall.GetStdHandle(stdOutputHandle)
	errHandle, errErr := syscall.GetStdHandle(stdErrorHandle)
	if inErr == nil && outErr == nil && errErr == nil && in != 0 && out != 0 && errHandle != 0 {
		si.StdInput = in
		si.StdOutput = out
		si.StdErr = errHandle
		si.Flags |= startfUseStdHandles
	}
}

// runInSilo launches cmdArgs[0] inside the silo job using
// PROC_THREAD_ATTRIBUTE_JOB_LIST, waits for it, and returns its exit code.
//
// If packageName is non-empty, PROC_THREAD_ATTRIBUTE_PACKAGE_FULL_NAME is
// also set, giving the process the package identity required by Desktop
// Bridge (MSIX-packaged Win32) applications such as winget.
//
// On build 26100 this fails with ERROR_INVALID_PARAMETER when the attribute
// references a silo job (hcsshim hits the same wall for job containers and
// uses the same suspended-create + assign fallback as runInSiloFallback
// below). The attribute path is tried first because it avoids the
// create-then-assign window entirely.
func runInSilo(job syscall.Handle, cmdArgs []string, detach bool, packageName string) (uint32, error) {
	// Build the command line. CreateProcess requires a mutable buffer; the
	// quoting rule follows the CRT/CommandLineToArgvW convention.
	cmdLine := buildCommandLine(cmdArgs)

	// Number of attributes: job list + mitigation policy, plus package name when given.
	attrCount := uint32(2)
	var pkgName16 []uint16
	if packageName != "" {
		attrCount = 3
		pkgName16 = syscall.StringToUTF16(packageName) // includes null terminator
	}

	var attrSize uintptr
	winapi.InitializeProcThreadAttributeList(nil, attrCount, 0, &attrSize)
	if attrSize == 0 {
		return 0, allowSiloFallback(errors.New("InitializeProcThreadAttributeList reported zero size"))
	}
	attrList := winapi.ProcThreadAttributeList(make([]byte, attrSize))
	if err := winapi.InitializeProcThreadAttributeList(attrList, attrCount, 0, &attrSize); err != nil {
		return 0, allowSiloFallback(fmt.Errorf("InitializeProcThreadAttributeList: %w", err))
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
		return 0, allowSiloFallback(fmt.Errorf("UpdateProcThreadAttribute(JOB_LIST): %w", err))
	}

	// Apply process hardening: reject remote-image loads and block legacy
	// extension-point DLL injection vectors (SetWindowsHookEx, AppInit_DLLs).
	// Best-effort: the silo job assignment is the primary security boundary.
	mitigationPolicy := winapi.DefaultChildMitigationPolicy1
	if err := winapi.UpdateProcThreadAttribute(
		attrList,
		0,
		winapi.PROC_THREAD_ATTRIBUTE_MITIGATION_POLICY,
		unsafe.Pointer(&mitigationPolicy),
		unsafe.Sizeof(mitigationPolicy),
	); err != nil {
		fmt.Fprintf(os.Stderr, "bindmount: mitigation policy not set (%v)\n", err)
	}

	if len(pkgName16) > 0 {
		if err := winapi.UpdateProcThreadAttribute(
			attrList,
			0,
			winapi.PROC_THREAD_ATTRIBUTE_PACKAGE_FULL_NAME,
			unsafe.Pointer(&pkgName16[0]),
			uintptr(len(pkgName16))*2,
		); err != nil {
			// Non-fatal: log and continue without package identity.
			fmt.Fprintf(os.Stderr, "bindmount: PACKAGE_FULL_NAME attribute not set (%v); app may fail to activate\n", err)
		}
	}

	var si winapi.StartupInfoEx
	si.StartupInfo.Cb = uint32(unsafe.Sizeof(si))
	si.AttributeList = unsafe.Pointer(&attrList[0])
	inheritStandardHandles(&si.StartupInfo)

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
		true,
		winapi.EXTENDED_STARTUPINFO_PRESENT,
		nil,
		nil,
		&si.StartupInfo,
		&pi,
	)
	if err != nil {
		createErr := fmt.Errorf("CreateProcess(%q): %w", cmdLine, err)
		if errors.Is(err, errorInvalidParameter) {
			return 0, allowSiloFallback(createErr)
		}
		return 0, createErr
	}
	defer syscall.CloseHandle(pi.Thread)
	defer syscall.CloseHandle(pi.Process)

	if detach {
		return 0, nil
	}

	// The child inherits our console and stdio handles; no redirection is
	// wired up, which is what a bind-mount helper wants: run the command as
	// if the caller had launched it, just inside the silo.

	if ret, waitErr := syscall.WaitForSingleObject(pi.Process, syscall.INFINITE); ret == waitFailed {
		return 0, fmt.Errorf("WaitForSingleObject: %w", waitErr)
	}

	var exitCode uint32
	if err := syscall.GetExitCodeProcess(pi.Process, &exitCode); err != nil {
		return 0, fmt.Errorf("GetExitCodeProcess: %w", err)
	}
	return exitCode, nil
}



// runInSiloFallback creates the process suspended, assigns it to the job with
// AssignProcessToJobObject, then resumes it. The suspended window matters:
// the process is assigned before its initial thread runs a single
// instruction, so it never observes the host filesystem view.
func runInSiloFallback(job syscall.Handle, cmdArgs []string, detach bool) (uint32, error) {
	cmdLine := buildCommandLine(cmdArgs)
	cmdPtr, err := syscall.UTF16PtrFromString(cmdLine)
	if err != nil {
		return 0, err
	}

	// Apply process hardening via a minimal attribute list. Unlike the primary
	// path, PROC_THREAD_ATTRIBUTE_JOB_LIST is not used here (it is precisely
	// that attribute whose failure drives us into this fallback). Mitigation
	// policies have been supported since Windows 8.1, so they succeed on the
	// same builds that reach this path.
	var si winapi.StartupInfoEx
	creationFlags := uint32(winapi.CREATE_SUSPENDED)

	var attrSize uintptr
	winapi.InitializeProcThreadAttributeList(nil, 1, 0, &attrSize)
	if attrSize > 0 {
		attrList := winapi.ProcThreadAttributeList(make([]byte, attrSize))
		if winapi.InitializeProcThreadAttributeList(attrList, 1, 0, &attrSize) == nil {
			mitigationPolicy := winapi.DefaultChildMitigationPolicy1
			if winapi.UpdateProcThreadAttribute(
				attrList, 0,
				winapi.PROC_THREAD_ATTRIBUTE_MITIGATION_POLICY,
				unsafe.Pointer(&mitigationPolicy),
				unsafe.Sizeof(mitigationPolicy),
			) == nil {
				defer winapi.DeleteProcThreadAttributeList(attrList)
				si.StartupInfo.Cb = uint32(unsafe.Sizeof(si))
				si.AttributeList = unsafe.Pointer(&attrList[0])
				creationFlags |= winapi.EXTENDED_STARTUPINFO_PRESENT
			} else {
				winapi.DeleteProcThreadAttributeList(attrList)
				fmt.Fprintf(os.Stderr, "bindmount: fallback mitigation policy not set\n")
			}
		}
	}
	if si.AttributeList == nil {
		// Attribute list setup failed; use plain STARTUPINFO size so Windows
		// does not attempt to read the (absent) attribute list pointer.
		si.StartupInfo.Cb = uint32(unsafe.Sizeof(si.StartupInfo))
	}
	inheritStandardHandles(&si.StartupInfo)

	var pi syscall.ProcessInformation
	err = syscall.CreateProcess(
		nil,
		cmdPtr,
		nil,
		nil,
		true,
		creationFlags,
		nil,
		nil,
		&si.StartupInfo,
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

	if _, err := winapi.ResumeThread(winapi.Handle(pi.Thread)); err != nil {
		syscall.TerminateProcess(pi.Process, 1)
		syscall.CloseHandle(pi.Thread)
		syscall.CloseHandle(pi.Process)
		return 0, fmt.Errorf("resume process: %w", err)
	}

	defer syscall.CloseHandle(pi.Thread)
	defer syscall.CloseHandle(pi.Process)
	if detach {
		return 0, nil
	}

	if ret, waitErr := syscall.WaitForSingleObject(pi.Process, syscall.INFINITE); ret == waitFailed {
		return 0, fmt.Errorf("WaitForSingleObject: %w", waitErr)
	}

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
