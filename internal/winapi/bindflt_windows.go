//go:build windows

package winapi

import (
	"sync"
	"syscall"
	"unsafe"
)

// ---------------------------------------------------------------------------
// bindfltapi.dll — undocumented Bf* interface (see docs/BindFilterAPI.md).
//
// The exports are resolved dynamically because Microsoft ships no import
// library for them. The DLL is loaded from %SystemRoot%\System32 only, to
// avoid DLL-planting on the search path.
// ---------------------------------------------------------------------------

var (
	loadOnce      sync.Once
	loadErr       error
	modbindfltapi *syscall.DLL

	procBfSetupFilter   *syscall.Proc
	procBfRemoveMapping *syscall.Proc
	procBfGetMappings   *syscall.Proc
)

// loadBindfltapi loads bindfltapi.dll from System32 and resolves the exports
// used by this package. It is called lazily and is safe for concurrent use;
// callers get ErrBindfltUnavailable when the DLL or a required export is
// missing.
func loadBindfltapi() error {
	loadOnce.Do(func() { loadErr = doLoadBindfltapi() })
	return loadErr
}

func doLoadBindfltapi() error {
	systemDir, err := syscall.UTF16PtrFromString(`%SystemRoot%\System32`)
	if err != nil {
		return err
	}
	var expanded [syscall.MAX_PATH]uint16
	n, err := expandEnvironmentStrings(systemDir, &expanded[0], uint32(len(expanded)))
	if err != nil {
		return err
	}
	if n == 0 || int(n) > len(expanded) {
		return ErrBindfltUnavailable
	}
	dllPath := syscall.UTF16ToString(expanded[:n]) + `\bindfltapi.dll`

	dll, err := syscall.LoadDLL(dllPath)
	if err != nil {
		return ErrBindfltUnavailable
	}

	lookup := func(name string) (*syscall.Proc, error) {
		p, err := dll.FindProc(name)
		if err != nil {
			return nil, err
		}
		return p, nil
	}

	if procBfSetupFilter, err = lookup("BfSetupFilter"); err != nil {
		return ErrBindfltUnavailable
	}
	if procBfRemoveMapping, err = lookup("BfRemoveMapping"); err != nil {
		return ErrBindfltUnavailable
	}
	if procBfGetMappings, err = lookup("BfGetMappings"); err != nil {
		return ErrBindfltUnavailable
	}
	modbindfltapi = dll
	return nil
}

// SetupFilter calls BfSetupFilter. job may be 0 for a global mapping.
func SetupFilter(job syscall.Handle, flags uint32, virtualRoot, virtualTarget string, exceptions []string) error {
	if err := loadBindfltapi(); err != nil {
		return err
	}

	rootPtr, err := syscall.UTF16PtrFromString(virtualRoot)
	if err != nil {
		return err
	}
	targetPtr, err := syscall.UTF16PtrFromString(virtualTarget)
	if err != nil {
		return err
	}

	var excPtr **uint16
	var excCount uint32
	var excSlice []*uint16
	if len(exceptions) > 0 {
		excSlice = make([]*uint16, len(exceptions))
		for i, e := range exceptions {
			p, err := syscall.UTF16PtrFromString(e)
			if err != nil {
				return err
			}
			excSlice[i] = p
		}
		excPtr = &excSlice[0]
		excCount = uint32(len(excSlice))
	}

	hr, _, _ := procBfSetupFilter.Call(
		uintptr(job),
		uintptr(flags),
		uintptr(unsafe.Pointer(rootPtr)),
		uintptr(unsafe.Pointer(targetPtr)),
		uintptr(unsafe.Pointer(excPtr)),
		uintptr(excCount),
	)
	if hr != 0 {
		return hresultError(hr)
	}
	return nil
}

// RemoveMapping calls BfRemoveMapping. job may be 0 for a global mapping.
func RemoveMapping(job syscall.Handle, virtualRoot string) error {
	if err := loadBindfltapi(); err != nil {
		return err
	}
	rootPtr, err := syscall.UTF16PtrFromString(virtualRoot)
	if err != nil {
		return err
	}
	hr, _, _ := procBfRemoveMapping.Call(
		uintptr(job),
		uintptr(unsafe.Pointer(rootPtr)),
	)
	if hr != 0 {
		return hresultError(hr)
	}
	return nil
}

// GetMappingsRaw calls BfGetMappings and returns the raw response buffer. The
// caller must validate every offset and length before interpreting it; see
// parseMappings for the checked version.
func GetMappingsRaw(flags uint32, job syscall.Handle, rootPath string, sid *SID, buffer []byte) (uint32, error) {
	if err := loadBindfltapi(); err != nil {
		return 0, err
	}

	var rootPtr *uint16
	var err error
	if rootPath != "" {
		rootPtr, err = syscall.UTF16PtrFromString(rootPath)
		if err != nil {
			return 0, err
		}
	}

	size := uint32(len(buffer))
	var outPtr *byte
	if len(buffer) > 0 {
		outPtr = &buffer[0]
	}

	hr, _, _ := procBfGetMappings.Call(
		uintptr(flags),
		uintptr(job),
		uintptr(unsafe.Pointer(rootPtr)),
		uintptr(unsafe.Pointer(sid)),
		uintptr(unsafe.Pointer(&size)),
		uintptr(unsafe.Pointer(outPtr)),
	)
	if hr != 0 {
		return size, hresultError(hr)
	}
	return size, nil
}

// expandEnvironmentStrings is kernel32!ExpandEnvironmentStringsW, bound
// locally to avoid depending on x/sys just for this helper.
var procexpandEnvironmentStrings = modkernel32.NewProc("ExpandEnvironmentStringsW")

func expandEnvironmentStrings(src *uint16, dst *uint16, size uint32) (uint32, error) {
	r, _, err := procexpandEnvironmentStrings.Call(
		uintptr(unsafe.Pointer(src)),
		uintptr(unsafe.Pointer(dst)),
		uintptr(size),
	)
	if r == 0 {
		return 0, err
	}
	return uint32(r), nil
}
