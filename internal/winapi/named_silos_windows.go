//go:build windows

package winapi

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"syscall"
	"unsafe"
)

const (
	directoryQuery        = 0x0001
	statusMoreEntries     = 0x00000105
	statusNoMoreEntries   = 0x8000001A
	objectDirectoryBuffer = 64 * 1024
)

var (
	modntdll                   = syscall.NewLazyDLL("ntdll.dll")
	procNtOpenDirectoryObject  = modntdll.NewProc("NtOpenDirectoryObject")
	procNtQueryDirectoryObject = modntdll.NewProc("NtQueryDirectoryObject")
)

type unicodeString struct {
	Length        uint16
	MaximumLength uint16
	Buffer        *uint16
}

type objectAttributes struct {
	Length             uintptr
	RootDirectory      uintptr
	ObjectName         *unicodeString
	Attributes         uintptr
	SecurityDescriptor uintptr
	SecurityQoS        uintptr
}

type objectDirectoryInformation struct {
	Name     unicodeString
	TypeName unicodeString
}

// NamedSilo identifies a named silo in the current caller's local or global
// object namespace.
type NamedSilo struct {
	Name string
	SiloBasicInformation
}

// ListVisibleNamedSilos returns the named Job Silos that the caller can query
// in its local and Global namespaces. Unnamed and inaccessible silos cannot
// be resolved to a Job Object name by Windows.
func ListVisibleNamedSilos() ([]NamedSilo, error) {
	directories := []struct {
		path   string
		prefix string
	}{
		{path: `\BaseNamedObjects`, prefix: `Global\`},
		{path: `\Sessions\` + fmt.Sprint(currentSessionID()) + `\BaseNamedObjects`},
	}

	seen := make(map[string]bool)
	var silos []NamedSilo
	var directoryErrors []error
	for _, directory := range directories {
		entries, err := listNamedJobs(directory.path)
		if err != nil {
			directoryErrors = append(directoryErrors, err)
			continue
		}
		for _, entry := range entries {
			name := directory.prefix + entry
			if seen[name] {
				continue
			}
			seen[name] = true
			job, err := OpenJob(name, JOB_OBJECT_QUERY)
			if err != nil {
				if errors.Is(err, syscall.ERROR_ACCESS_DENIED) ||
					errors.Is(err, syscall.ERROR_FILE_NOT_FOUND) ||
					errors.Is(err, syscall.ERROR_PATH_NOT_FOUND) {
					continue
				}
				return nil, fmt.Errorf("open named job %q: %w", name, err)
			}
			info, infoErr := QuerySiloBasicInformation(job)
			syscall.CloseHandle(job)
			if infoErr != nil {
				continue
			}
			silos = append(silos, NamedSilo{Name: name, SiloBasicInformation: info})
		}
	}
	if len(silos) == 0 && len(directoryErrors) == len(directories) {
		return nil, fmt.Errorf("enumerate named Job Silos: %w", errors.Join(directoryErrors...))
	}
	sort.Slice(silos, func(i, j int) bool {
		return strings.Compare(strings.ToLower(silos[i].Name), strings.ToLower(silos[j].Name)) < 0
	})
	return silos, nil
}

func currentSessionID() uint32 {
	var sessionID uint32
	procProcessIDToSessionID := modkernel32.NewProc("ProcessIdToSessionId")
	r, _, _ := procProcessIDToSessionID.Call(uintptr(syscall.Getpid()), uintptr(unsafe.Pointer(&sessionID)))
	if r == 0 {
		return 0
	}
	return sessionID
}

func listNamedJobs(path string) ([]string, error) {
	path16, err := syscall.UTF16FromString(path)
	if err != nil {
		return nil, err
	}
	objectName := unicodeString{
		Length:        uint16((len(path16) - 1) * 2),
		MaximumLength: uint16((len(path16) - 1) * 2),
		Buffer:        &path16[0],
	}
	attributes := objectAttributes{
		Length:     unsafe.Sizeof(objectAttributes{}),
		ObjectName: &objectName,
	}
	var directory Handle
	status, _, _ := procNtOpenDirectoryObject.Call(
		uintptr(unsafe.Pointer(&directory)),
		directoryQuery,
		uintptr(unsafe.Pointer(&attributes)),
	)
	if status != 0 {
		return nil, ntStatusError("NtOpenDirectoryObject", uint32(status))
	}
	defer syscall.CloseHandle(directory)

	buffer := make([]byte, objectDirectoryBuffer)
	var context, returnLength uint32
	var names []string
	for {
		status, _, _ = procNtQueryDirectoryObject.Call(
			uintptr(directory),
			uintptr(unsafe.Pointer(&buffer[0])),
			uintptr(len(buffer)),
			0,
			0,
			uintptr(unsafe.Pointer(&context)),
			uintptr(unsafe.Pointer(&returnLength)),
		)
		if uint32(status) == statusNoMoreEntries {
			break
		}
		if status != 0 && uint32(status) != statusMoreEntries {
			return nil, ntStatusError("NtQueryDirectoryObject", uint32(status))
		}
		entries, err := jobNamesFromDirectoryBuffer(buffer)
		if err != nil {
			return nil, err
		}
		names = append(names, entries...)
		if status == 0 {
			continue
		}
	}
	return names, nil
}

func jobNamesFromDirectoryBuffer(buffer []byte) ([]string, error) {
	entrySize := unsafe.Sizeof(objectDirectoryInformation{})
	if entrySize == 0 {
		return nil, errors.New("zero object-directory entry size")
	}
	var names []string
	for offset := uintptr(0); offset+entrySize <= uintptr(len(buffer)); offset += entrySize {
		entry := (*objectDirectoryInformation)(unsafe.Pointer(&buffer[offset]))
		if entry.Name.Length == 0 {
			return names, nil
		}
		name, err := objectDirectoryString(entry.Name, buffer)
		if err != nil {
			return nil, err
		}
		typeName, err := objectDirectoryString(entry.TypeName, buffer)
		if err != nil {
			return nil, err
		}
		if typeName == "Job" {
			names = append(names, name)
		}
	}
	return nil, errors.New("object-directory response is missing its terminator")
}

func objectDirectoryString(value unicodeString, buffer []byte) (string, error) {
	if value.Length%2 != 0 {
		return "", errors.New("object-directory string has an odd byte length")
	}
	if value.Length == 0 {
		return "", nil
	}
	if value.Buffer == nil {
		return "", errors.New("object-directory string has a nil buffer")
	}
	start := uintptr(unsafe.Pointer(value.Buffer))
	base := uintptr(unsafe.Pointer(&buffer[0]))
	end := start + uintptr(value.Length)
	limit := base + uintptr(len(buffer))
	if start < base || end < start || end > limit {
		return "", errors.New("object-directory string points outside its response")
	}
	return syscall.UTF16ToString(unsafe.Slice(value.Buffer, int(value.Length/2))), nil
}

func ntStatusError(operation string, status uint32) error {
	return fmt.Errorf("%s: NTSTATUS 0x%08X", operation, status)
}
