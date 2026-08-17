//go:build !windows

package winapi

// Stub constants so cross-platform packages can compile.
const (
	BINDFLT_FLAG_READ_ONLY_MAPPING        uint32 = 0x00000001
	BINDFLT_FLAG_MERGED_BIND_MAPPING      uint32 = 0x00000002
	BINDFLT_FLAG_USE_CURRENT_SILO_MAPPING uint32 = 0x00000004
	BINDFLT_FLAG_NO_MULTIPLE_TARGETS      uint32 = 0x00000040
	BINDFLT_GET_MAPPINGS_FLAG_VOLUME      uint32 = 0x00000001
	BINDFLT_GET_MAPPINGS_FLAG_SILO        uint32 = 0x00000002
	BINDFLT_GET_MAPPINGS_FLAG_USER        uint32 = 0x00000004
)

// Mapping mirrors the Windows build's decoded form.
type Mapping struct {
	VirtualRoot string
	Flags       uint32
	Targets     []string
}

// NTPathToDOS stub.
func NTPathToDOS(p string) (string, error) { return p, nil }

// NTVirtualRootToDOS stub.
func NTVirtualRootToDOS(p string) string { return p }

// ParseMappingsChecked stub.
func ParseMappingsChecked(buf []byte, size uint32) ([]Mapping, error) { return nil, nil }

// Job object stubs. Handle is uintptr here; the Windows build uses
// syscall.Handle which has the same representation.
type Handle = uintptr

func LogicalDriveLetters() ([]rune, error) { return nil, errENOSYS }

func CreateJob(name string) (Handle, error)              { return 0, errENOSYS }
func OpenJob(name string, access uint32) (Handle, error) { return 0, errENOSYS }
func SetKillOnJobClose(job Handle) error                  { return errENOSYS }
func SetJobUIRestrictions(job Handle, flags uint32) error { return errENOSYS }
func PromoteToSilo(job Handle) error                      { return errENOSYS }
func AssignProcessToJob(job, process Handle) error       { return errENOSYS }
func ResumeThread(_ Handle) (uint32, error)             { return 0, errENOSYS }

// ReadAppExecLink stub — APPEXECLINK reparse points only exist on Windows.
func ReadAppExecLink(_ string) (string, error) { return "", errENOSYS }

// AppExecLinkInfo stub.
type AppExecLinkInfo struct {
	PackageFullName string
	ExePath         string
}

// ReadAppExecLinkInfo stub.
func ReadAppExecLinkInfo(_ string) (*AppExecLinkInfo, error) { return nil, errENOSYS }

var errENOSYS = errorString("function not implemented on this platform")

type errorString string

func (e errorString) Error() string { return string(e) }
