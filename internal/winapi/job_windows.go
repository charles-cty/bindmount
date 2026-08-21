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
	JobObjectBasicUIRestrictions      = 4
	JobObjectCreateSilo               = 35 // JOBOBJECTINFOCLASS value used by hcsshim
	JobObjectExtendedLimitInformation = 9
	JobObjectSiloBasicInformation     = 36

	JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE = 0x2000

	// UI-restriction flags for JobObjectBasicUIRestrictions
	// (JOBOBJECT_BASIC_UI_RESTRICTIONS.UIRestrictionsClass).
	JOB_OBJECT_UILIMIT_SYSTEMPARAMETERS = 0x00000008 // block SystemParametersInfo changes
	JOB_OBJECT_UILIMIT_DISPLAYSETTINGS  = 0x00000010 // block ChangeDisplaySettings
	JOB_OBJECT_UILIMIT_DESKTOP          = 0x00000040 // block CreateDesktop / SwitchDesktop
	JOB_OBJECT_UILIMIT_EXITWINDOWS      = 0x00000080 // block ExitWindows / ExitWindowsEx
)

var (
	procCreateJobObjectW          = modkernel32.NewProc("CreateJobObjectW")
	procOpenJobObjectW            = modkernel32.NewProc("OpenJobObjectW")
	procSetInformationJobObject   = modkernel32.NewProc("SetInformationJobObject")
	procQueryInformationJobObject = modkernel32.NewProc("QueryInformationJobObject")
	procAssignProcessToJobObject  = modkernel32.NewProc("AssignProcessToJobObject")
	procIsProcessInJob            = modkernel32.NewProc("IsProcessInJob")
	procSetHandleInformation      = modkernel32.NewProc("SetHandleInformation")
	procTerminateJobObject        = modkernel32.NewProc("TerminateJobObject")
)

const handleFlagInherit = 0x00000001

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
// a non-empty plain name is created in the caller's session namespace — prefix
// with "Global\" to create in the global namespace (session 0), which is where
// elevated processes' objects are visible across sessions.
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

// SetJobLimitFlags sets the LimitFlags field of the job's extended limit
// information, replacing the current flag word. All other fields in
// JOBOBJECT_EXTENDED_LIMIT_INFORMATION (working-set limits, process count,
// memory caps, I/O counters) are written as zero; this function is intended
// only for freshly created jobs before any other limits are configured.
// Callers combine JOB_OBJECT_LIMIT_* constants.
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

// IsProcessInJob reports whether process belongs to job, including through a
// nested job relationship.
func IsProcessInJob(process, job Handle) (bool, error) {
	var inJob int32
	r, _, callErr := procIsProcessInJob.Call(
		uintptr(process),
		uintptr(job),
		uintptr(unsafe.Pointer(&inJob)),
	)
	if r == 0 {
		if callErr != syscall.Errno(0) {
			return false, callErr
		}
		return false, fmt.Errorf("IsProcessInJob failed")
	}
	return inJob != 0, nil
}

// MakeHandleInheritable allows a detached child to keep the Job Object alive
// after bindmount exits. Descendants keep it only when their creator enables
// handle inheritance; a dedicated keeper is needed for stronger guarantees.
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

// jobobjectBasicUIRestrictions mirrors JOBOBJECT_BASIC_UI_RESTRICTIONS.
type jobobjectBasicUIRestrictions struct {
	UIRestrictionsClass uint32
}

// SiloBasicInformation identifies a Job Silo and describes its current
// process membership.
type SiloBasicInformation struct {
	SiloID            uint32
	SiloParentID      uint32
	NumberOfProcesses uint32
	IsInServerSilo    bool
	Reserved          [3]uint8
}

// QuerySiloBasicInformation returns the identity and basic state of a Job
// Silo. The call fails when job is not a silo.
func QuerySiloBasicInformation(job Handle) (SiloBasicInformation, error) {
	var info SiloBasicInformation
	r, _, callErr := procQueryInformationJobObject.Call(
		uintptr(job),
		uintptr(JobObjectSiloBasicInformation),
		uintptr(unsafe.Pointer(&info)),
		uintptr(unsafe.Sizeof(info)),
		0,
	)
	if r == 0 {
		if callErr != syscall.Errno(0) {
			return SiloBasicInformation{}, callErr
		}
		return SiloBasicInformation{}, fmt.Errorf("QueryInformationJobObject(SiloBasicInformation) failed")
	}
	return info, nil
}

// SetJobUIRestrictions configures the UI-restriction class on the job via
// JobObjectBasicUIRestrictions. Call this before assigning processes: some
// restrictions are enforced at process-attach time and cannot be applied
// retroactively.
// Callers combine JOB_OBJECT_UILIMIT_* constants.
func SetJobUIRestrictions(job Handle, flags uint32) error {
	info := jobobjectBasicUIRestrictions{UIRestrictionsClass: flags}
	r, _, callErr := procSetInformationJobObject.Call(
		uintptr(job),
		uintptr(JobObjectBasicUIRestrictions),
		uintptr(unsafe.Pointer(&info)),
		uintptr(unsafe.Sizeof(info)),
	)
	if r == 0 {
		if callErr != syscall.Errno(0) {
			return callErr
		}
		return fmt.Errorf("SetInformationJobObject(BasicUIRestrictions) failed")
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
