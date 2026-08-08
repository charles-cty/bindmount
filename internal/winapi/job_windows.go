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

// SetKillOnJobClose configures the job to terminate its processes when the
// last handle is closed.
func SetKillOnJobClose(job Handle) error {
	var info jobobjectExtendedLimitInformation
	info.BasicLimitInformation.LimitFlags = JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
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
