//go:build windows

package winapi

import (
	"fmt"
	"syscall"
	"unsafe"
)

// Job object bindings used for silo-scoped mappings. Only the operations the
// CLI needs are wrapped: create/open, promote to silo, set kill-on-close, and
// query the silo state.

const (
	JobObjectCreateSilo               = 35 // JOBOBJECTINFOCLASS value used by hcsshim
	JobObjectExtendedLimitInformation = 9
	JobObjectSiloBasicInformation     = 37

	JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE = 0x2000
)

var (
	procCreateJobObjectW         = modkernel32.NewProc("CreateJobObjectW")
	procOpenJobObjectW           = modkernel32.NewProc("OpenJobObjectW")
	procSetInformationJobObject  = modkernel32.NewProc("SetInformationJobObject")
	procAssignProcessToJobObject = modkernel32.NewProc("AssignProcessToJobObject")
	procSetHandleInformation     = modkernel32.NewProc("SetHandleInformation")
	procTerminateJobObject       = modkernel32.NewProc("TerminateJobObject")
)

const (
	handleFlagInherit = 0x00000001
)

// JOBOBJECT_BASIC_LIMIT_INFORMATION + IO counters, matching the public SDK
// layout of JOBOBJECT_EXTENDED_LIMIT_INFORMATION.
type jobobjectBasicLimitInformation struct {
	PerProcessUserTimeLimit uint64
	PerJobUserTimeLimit     uint64
	LimitFlags              uint32
	MinimumWorkingSetSize   uintptr
	MaximumWorkingSetSize   uintptr
	ActiveProcessLimit      uint32
	Affinity                uintptr
	PriorityClass           uint32
	SchedulingClass         uint32
}

type ioCounters struct {
	ReadOperationCount  uint64
	WriteOperationCount uint64
	OtherOperationCount uint64
	ReadTransferCount   uint64
	WriteTransferCount  uint64
	OtherTransferCount  uint64
}

type jobobjectExtendedLimitInformation struct {
	BasicLimitInformation jobobjectBasicLimitInformation
	IoInfo                ioCounters
	ProcessMemoryLimit    uintptr
	JobMemoryLimit        uintptr
	PeakProcessMemoryUsed uintptr
	PeakJobMemoryUsed     uintptr
}

// CreateJob creates a new job object. name may be empty for an unnamed job;
// a non-empty name is created in the Global\ namespace when it has no prefix.
// Returns an error if the name already exists, preventing silent reuse of an
// existing job that may already hold processes.
func CreateJob(name string) (Handle, error) {
	var namePtr *uint16
	var err error
	if name != "" {
		namePtr, err = syscall.UTF16PtrFromString(name)
		if err != nil {
			return 0, err
		}
	}
	h, _, callErr := procCreateJobObjectW.Call(0, uintptr(unsafe.Pointer(namePtr)))
	if h == 0 {
		if callErr != syscall.Errno(0) {
			return 0, callErr
		}
		return 0, fmt.Errorf("CreateJobObjectW failed")
	}
	// CreateJobObjectW succeeds even when the name already exists, returning
	// the existing handle. Detect this via ERROR_ALREADY_EXISTS and refuse to
	// reuse the existing job, which may already contain foreign processes.
	if callErr == syscall.ERROR_ALREADY_EXISTS {
		syscall.CloseHandle(syscall.Handle(h))
		return 0, fmt.Errorf("job %q already exists", name)
	}
	return Handle(h), nil
}

// OpenJob opens an existing job object by name (e.g. "Global\\ExampleSilo"
// or a plain name). desiredAccess should include at least
// JOB_OBJECT_ALL_ACCESS-equivalent rights for mapping operations.
func OpenJob(name string, desiredAccess uint32) (Handle, error) {
	namePtr, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return 0, err
	}
	h, _, callErr := procOpenJobObjectW.Call(
		uintptr(desiredAccess),
		0, // do not inherit
		uintptr(unsafe.Pointer(namePtr)),
	)
	if h == 0 {
		if callErr != syscall.Errno(0) {
			return 0, callErr
		}
		return 0, fmt.Errorf("OpenJobObjectW(%q) failed", name)
	}
	return Handle(h), nil
}

// SetJobLimitFlags sets the given JOBOBJECT_EXTENDED_LIMIT_INFORMATION
// LimitFlags on the job, replacing the current flag word. Callers combine
// JOB_OBJECT_LIMIT_* constants.
func SetJobLimitFlags(job Handle, flags uint32) error {
	// A silo must not permit either explicit (CREATE_BREAKAWAY_FROM_JOB) or
	// silent breakaway. Keep this invariant at the Win32 wrapper boundary so a
	// future caller cannot accidentally turn a silo into an escapable job.
	if flags&(JOB_OBJECT_LIMIT_BREAKAWAY_OK|JOB_OBJECT_LIMIT_SILENT_BREAKAWAY_OK) != 0 {
		return fmt.Errorf("breakaway job limits are not permitted for silos")
	}
	var info jobobjectExtendedLimitInformation
	info.BasicLimitInformation.LimitFlags = flags
	r, _, callErr := procSetInformationJobObject.Call(
		uintptr(job),
		uintptr(JobObjectExtendedLimitInformation),
		uintptr(unsafe.Pointer(&info)),
		uintptr(unsafe.Sizeof(info)),
	)
	if r == 0 {
		if callErr != syscall.Errno(0) {
			return callErr
		}
		return fmt.Errorf("SetInformationJobObject(ExtendedLimitInformation) failed")
	}
	return nil
}

// SetKillOnJobClose configures the job to terminate its processes when the
// last handle is closed.
func SetKillOnJobClose(job Handle) error {
	return SetJobLimitFlags(job, JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE)
}

// PromoteToSilo promotes an empty job object to a silo. The job must not
// already contain processes.
func PromoteToSilo(job Handle) error {
	r, _, callErr := procSetInformationJobObject.Call(
		uintptr(job),
		uintptr(JobObjectCreateSilo),
		0,
		0,
	)
	if r == 0 {
		if callErr != syscall.Errno(0) {
			return callErr
		}
		return fmt.Errorf("SetInformationJobObject(CreateSilo) failed")
	}
	return nil
}

// AssignProcessToJob associates a process with the job.
func AssignProcessToJob(job, process Handle) error {
	r, _, callErr := procAssignProcessToJobObject.Call(uintptr(job), uintptr(process))
	if r == 0 {
		if callErr != syscall.Errno(0) {
			return callErr
		}
		return fmt.Errorf("AssignProcessToJobObject failed")
	}
	return nil
}

// MakeHandleInheritable allows a detached child to keep the Job Object alive
// after bindmount exits. The child and its descendants inherit the handle;
// when the workload ends, the last handle closes and KILL_ON_JOB_CLOSE tears
// down the silo.
func MakeHandleInheritable(handle Handle) error {
	r, _, callErr := procSetHandleInformation.Call(
		uintptr(handle), handleFlagInherit, handleFlagInherit)
	if r == 0 {
		if callErr != syscall.Errno(0) {
			return callErr
		}
		return fmt.Errorf("SetHandleInformation failed")
	}
	return nil
}

// TerminateJob terminates every process currently assigned to the job.
func TerminateJob(job Handle, exitCode uint32) error {
	r, _, callErr := procTerminateJobObject.Call(uintptr(job), uintptr(exitCode))
	if r == 0 {
		if callErr != syscall.Errno(0) {
			return callErr
		}
		return fmt.Errorf("TerminateJobObject failed")
	}
	return nil
}
