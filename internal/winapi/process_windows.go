//go:build windows

package winapi

import (
	"errors"
	"syscall"
	"unsafe"
)

// Process-creation support for launching a process directly into a job via
// PROC_THREAD_ATTRIBUTE_JOB_LIST. These are public, documented Win32 APIs.

// The SDK macro:
//
//	#define ProcThreadAttributeValue(Number, Thread, Input, Additive) \
//	    (((Number) & PROC_THREAD_ATTRIBUTE_NUMBER) | \
//	     ((Thread != FALSE) ? PROC_THREAD_ATTRIBUTE_THREAD : 0) | \
//	     ((Input != FALSE) ? PROC_THREAD_ATTRIBUTE_INPUT : 0) | \
//	     ((Additive == TRUE) ? PROC_THREAD_ATTRIBUTE_ADDITIVE : 0))
//
// with PROC_THREAD_ATTRIBUTE_NUMBER = 0x0000ffff, THREAD = 0x00010000,
// INPUT = 0x00020000, ADDITIVE = 0x00040000.
// JOB_LIST: Number=13, Thread=FALSE, Input=TRUE, Additive=FALSE
//
//	=> 13 | 0x00020000 = 0x0002000D
const PROC_THREAD_ATTRIBUTE_JOB_LIST = 13 | 0x00020000

// PROC_THREAD_ATTRIBUTE_PACKAGE_FULL_NAME sets the package full name on the
// launched process, giving it the package identity needed by Desktop Bridge
// (MSIX-packaged Win32) applications. Value 29, Input=TRUE, Thread=FALSE.
const PROC_THREAD_ATTRIBUTE_PACKAGE_FULL_NAME = 29 | 0x00020000

const EXTENDED_STARTUPINFO_PRESENT = 0x00080000

var (
	procInitializeProcThreadAttributeList = modkernel32.NewProc("InitializeProcThreadAttributeList")
	procUpdateProcThreadAttribute         = modkernel32.NewProc("UpdateProcThreadAttribute")
	procDeleteProcThreadAttributeList     = modkernel32.NewProc("DeleteProcThreadAttributeList")
)

var procGetLogicalDrives = modkernel32.NewProc("GetLogicalDrives")

// LogicalDriveLetters returns drive letters currently visible to the caller.
func LogicalDriveLetters() ([]rune, error) {
	mask, _, callErr := procGetLogicalDrives.Call()
	if mask == 0 {
		if callErr != syscall.Errno(0) {
			return nil, callErr
		}
		return nil, syscall.EINVAL
	}
	letters := make([]rune, 0, 26)
	for i := 0; i < 26; i++ {
		if mask&(uintptr(1)<<i) != 0 {
			letters = append(letters, rune('A'+i))
		}
	}
	return letters, nil
}

// ProcThreadAttributeList is an opaque pointer to a caller-owned buffer.
type ProcThreadAttributeList []byte

// InitializeProcThreadAttributeList sizes (attrList == nil) or initializes an
// attribute list in attrList.
func InitializeProcThreadAttributeList(attrList ProcThreadAttributeList, attrCount uint32, flags uint32, size *uintptr) error {
	var ptr uintptr
	if attrList != nil {
		ptr = uintptr(unsafe.Pointer(&attrList[0]))
	}
	r, _, err := procInitializeProcThreadAttributeList.Call(
		ptr,
		uintptr(attrCount),
		uintptr(flags),
		uintptr(unsafe.Pointer(size)),
	)
	// When sizing (attrList == nil) the function is documented to fail with
	// ERROR_INSUFFICIENT_BUFFER while still writing *size; treat a non-zero
	// size as success in that mode.
	if r == 0 {
		if attrList == nil && *size != 0 {
			return nil
		}
		if err != syscall.Errno(0) {
			return err
		}
		return syscall.EINVAL
	}
	return nil
}

// UpdateProcThreadAttribute adds the job-list attribute to the list.
func UpdateProcThreadAttribute(attrList ProcThreadAttributeList, flags uint32, attribute uintptr, value unsafe.Pointer, size uintptr) error {
	r, _, err := procUpdateProcThreadAttribute.Call(
		uintptr(unsafe.Pointer(&attrList[0])),
		uintptr(flags),
		attribute,
		uintptr(value),
		size,
		0,
		0,
	)
	if r == 0 {
		if err != syscall.Errno(0) {
			return err
		}
		return syscall.EINVAL
	}
	return nil
}

// DeleteProcThreadAttributeList frees the list (the buffer remains owned by
// the caller).
func DeleteProcThreadAttributeList(attrList ProcThreadAttributeList) {
	procDeleteProcThreadAttributeList.Call(uintptr(unsafe.Pointer(&attrList[0])))
}

var procResumeThread = modkernel32.NewProc("ResumeThread")

// ResumeThread decrements the thread's suspend count. Returns the previous
// suspend count; a return value of 0xFFFFFFFF signals failure.
func ResumeThread(thread Handle) (uint32, error) {
	r, _, err := procResumeThread.Call(uintptr(thread))
	if r == 0xFFFFFFFF {
		if err != syscall.Errno(0) {
			return 0, err
		}
		return 0, errors.New("ResumeThread failed")
	}
	return uint32(r), nil
}

// StartupInfoEx mirrors STARTUPINFOEXW. AttributeList must hold the data
// pointer of the attribute-list buffer (obtained from
// InitializeProcThreadAttributeList), matching the SDK's
// LPPROC_THREAD_ATTRIBUTE_LIST field.
type StartupInfoEx struct {
	StartupInfo   syscall.StartupInfo
	AttributeList unsafe.Pointer // LPPROC_THREAD_ATTRIBUTE_LIST
}
