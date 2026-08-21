//go:build windows

package winapi

import "syscall"

// BfSetupFilter flags. The public Bindlink API documents NONE/READ_ONLY/MERGED;
// the remaining values are recovered internal names described in
// docs/BindFilterAPI.md and must not be treated as a stable contract.
const (
	BINDFLT_FLAG_READ_ONLY_MAPPING              uint32 = 0x00000001
	BINDFLT_FLAG_MERGED_BIND_MAPPING            uint32 = 0x00000002
	BINDFLT_FLAG_USE_CURRENT_SILO_MAPPING       uint32 = 0x00000004
	BINDFLT_FLAG_REPARSE_ON_FILES               uint32 = 0x00000008 // internal name
	BINDFLT_FLAG_SKIP_SHARING_CHECK             uint32 = 0x00000010 // internal name; rejected by BfSetupFilter on build 26100
	BINDFLT_FLAG_CLOUD_FILES_ECPS               uint32 = 0x00000020 // internal name
	BINDFLT_FLAG_NO_MULTIPLE_TARGETS            uint32 = 0x00000040
	BINDFLT_FLAG_IMMUTABLE_BACKING              uint32 = 0x00000080 // internal name
	BINDFLT_FLAG_PREVENT_CASE_SENSITIVE_BINDING uint32 = 0x00000100 // internal name
	BINDFLT_FLAG_EMPTY_VIRT_ROOT                uint32 = 0x00000200 // internal name
	BINDFLT_FLAG_NO_REPARSE_ON_ROOT             uint32 = 0x10000000 // internal name
	BINDFLT_FLAG_BATCHED_REMOVE_MAPPINGS        uint32 = 0x20000000 // internal name
)

// BfGetMappings flags.
const (
	BINDFLT_GET_MAPPINGS_FLAG_VOLUME uint32 = 0x00000001
	BINDFLT_GET_MAPPINGS_FLAG_SILO   uint32 = 0x00000002
	BINDFLT_GET_MAPPINGS_FLAG_USER   uint32 = 0x00000004
)

// Job object access rights (public Win32 constants).
const (
	JOB_OBJECT_ALL_ACCESS = 0x1F001F
	JOB_OBJECT_QUERY      = 0x0004
	JOB_OBJECT_TERMINATE  = 0x0008

	PROCESS_QUERY_INFORMATION = 0x0400
)

// Handle is the job/process handle type used across this package. On Windows
// it is syscall.Handle; the alias keeps signatures identical on both builds.
type Handle = syscall.Handle

// Process creation flags not exported by the syscall package.
const (
	CREATE_SUSPENDED = 0x00000004
)

// Breakaway-related JOBOBJECT_EXTENDED_LIMIT_INFORMATION.LimitFlags. These
// must remain clear for a silo whose process tree is not allowed to escape.
// CREATE_BREAKAWAY_FROM_JOB is rejected unless the containing job opts in via
// JOB_OBJECT_LIMIT_BREAKAWAY_OK. SILENT_BREAKAWAY_OK permits an implicit
// breakaway and is therefore equally unsafe here.
const (
	JOB_OBJECT_LIMIT_BREAKAWAY_OK        = 0x00000800
	JOB_OBJECT_LIMIT_SILENT_BREAKAWAY_OK = 0x00001000
)
