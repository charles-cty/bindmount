# Bind Filter API (bindfltapi.dll)

## Important

This page describes the `Bf*` exports observed in the Windows container code used by
[`hcsshim`](https://github.com/microsoft/hcsshim) and
[`go-winio`](https://github.com/microsoft/go-winio). It is written in the style of a
Win32 API reference, but it is not an official Microsoft API reference.

The declarations and flags of such APIs are not present in the public Windows
SDK documentation. Microsoft does document the newer
[Bindlink API](https://learn.microsoft.com/en-us/windows/win32/bindlink/), which uses
`bindlink.dll` and ultimately relies on the `bindflt.sys` minifilter. The APIs described
here are a lower-level, system-internal interface used by Windows components and other
Microsoft projects.

Applications should prefer the documented Bindlink API when it provides the required
semantics. Do not redistribute `bindfltapi.dll`, assume that its ABI is stable across
Windows releases, or treat the declarations below as a supported third-party contract.

## In this article

- [Purpose](#purpose)
- [API functions](#api-functions)
  - [BfSetupFilter](#bfsetupfilter)
  - [BfRemoveMapping](#bfremovemapping)
  - [BfGetMappings](#bfgetmappings)
- [BfSetupFilter flags](#bfsetupfilter-flags)
- [Observed merged-bind behavior](#observed-merged-bind-behavior)
- [BfGetMappings flags](#bfgetmappings-flags)
- [BfGetMappings buffer format](#bfgetmappings-buffer-format)
- [Mapping scope](#mapping-scope)
- [Silo-scoped bind links](#silo-scoped-bind-links)
- [Examples](#examples)
- [Requirements](#requirements)
- [See also](#see-also)

## Purpose

The Bind Filter is a filesystem minifilter implemented by `bindflt.sys`. It presents a
backing file, directory, or volume at a different virtual path. The virtual path is
resolved by the filter when a process accesses it; the operation does not copy the
backing data and does not require creating a normal directory junction at the virtual
path.

The `Bf*` functions configure mappings in the Bind Filter:

| Function | Operation |
| --- | --- |
| `BfSetupFilter` | Creates a mapping from a virtual root to a backing target. |
| `BfRemoveMapping` | Removes a mapping identified by its scope and virtual root. |
| `BfGetMappings` | Retrieves mappings into a caller-provided binary buffer. |

The hcsshim job-container implementation uses `BfSetupFilter` with a Windows job
handle and silo flags. As a result, the mapping is visible to processes in that job or
silo and is not visible in the ordinary host filesystem view. The hcsshim implementation
does not call `BfRemoveMapping`; it relies on the lifetime of the job/silo mapping.

The API does not expose the driver protocol used between `bindfltapi.dll` and
`bindflt.sys`. Callers should use the exports described here rather than attempting to
open the driver or invent an `FSCTL`/IOCTL protocol.

## API functions

### BfSetupFilter

Creates a Bind Filter mapping from a virtual root path to a backing target path.

#### Syntax

```cpp
HRESULT WINAPI BfSetupFilter(
    _In_opt_ HANDLE   JobHandle,
    _In_     ULONG    Flags,
    _In_     LPCWSTR  VirtualizationRootPath,
    _In_     LPCWSTR  VirtualizationTargetPath,
    _In_reads_opt_(VirtualizationExceptionPathCount)
             LPCWSTR *VirtualizationExceptionPaths,
    _In_opt_ ULONG    VirtualizationExceptionPathCount
);
```

#### Parameters

`JobHandle`

An optional handle to the job object whose mapping namespace should receive the
binding.

Pass `NULL` or `0` to create a global mapping. Pass a valid job handle to associate the
mapping with that job. For silo-local mappings, the job must already represent a silo;
the caller normally also specifies
`BINDFLT_FLAG_USE_CURRENT_SILO_MAPPING`.

The exact access rights required on the handle are implementation-defined. The caller
must retain a usable handle for operations that identify or remove the mapping by job
scope.

`Flags`

Flags that control the mapping. See [BfSetupFilter flags](#bfsetupfilter-flags).

`VirtualizationRootPath`

The path at which the backing target is presented. This is the virtual root from the
point of view of processes in the selected mapping scope.

The parent directory of this path must already exist. The hcsshim and go-winio callers
create the parent directory before calling `BfSetupFilter`. The path may identify a
file or a directory depending on the target and the behavior supported by the installed
Bind Filter version.

If a mapping is created over an existing directory, the mapping can shadow the existing
directory in the applicable filesystem view.

`VirtualizationTargetPath`

The backing file, directory, or volume to present at
`VirtualizationRootPath`.

Volume paths may require a trailing backslash when they are mapped to a directory. The
go-winio helper normalizes volume paths before calling the function; hcsshim generally
passes the volume path supplied by its layer-mounting code.

`VirtualizationExceptionPaths`

An optional array of null-terminated paths that are excluded from the mapping. Pass
`NULL` when no exceptions are required.

The exception semantics and path matching rules are not publicly specified. The
hcsshim call passes `NULL` and a count of zero.

`VirtualizationExceptionPathCount`

The number of entries in `VirtualizationExceptionPaths`. Pass zero when the exception
array is `NULL`.

#### Return value

Returns an `HRESULT`. `S_OK` indicates success. A failing HRESULT indicates that the
mapping could not be created.

The generated Go bindings in this repository convert a failing HRESULT to a Go error.
They do not use `GetLastError` to obtain the result of the operation.

#### Remarks

The query response format can represent multiple targets, and
`BINDFLT_FLAG_NO_MULTIPLE_TARGETS` asks the filter to reject a mapping that would
produce multiple targets. This does not imply that repeated `BfSetupFilter` calls append
targets to an existing virtual root. On Windows build 26100, a second global setup call
for the same virtual root returned `E_INVALIDARG`, both with and without merged-bind
flags. Multiple targets can instead arise from mapping composition; the exact creation
rules are not publicly specified.

`BINDFLT_FLAG_MERGED_BIND_MAPPING` merges the existing contents of a directory virtual
root with the backing target. It does not implement copy-on-write, materialization, or
whiteouts. See [Observed merged-bind behavior](#observed-merged-bind-behavior).

The hcsshim silo path uses the following flags for a writable mapping:

```cpp
BINDFLT_FLAG_USE_CURRENT_SILO_MAPPING
```

For a read-only mapping, it adds:

```cpp
BINDFLT_FLAG_USE_CURRENT_SILO_MAPPING |
BINDFLT_FLAG_READ_ONLY_MAPPING
```

The function configures the filter. It does not mount a volume in the traditional
Windows volume-mount-point sense, and it does not create a Linux-style mount namespace.

### BfRemoveMapping

Removes the mapping for a virtual root in the specified mapping scope.

#### Syntax

```cpp
HRESULT WINAPI BfRemoveMapping(
    _In_opt_ HANDLE  JobHandle,
    _In_     LPCWSTR VirtualizationRootPath
);
```

#### Parameters

`JobHandle`

The mapping scope used when the mapping was created. Pass `NULL` or `0` for a global
mapping. For a job- or silo-scoped mapping, pass the corresponding job handle.

`VirtualizationRootPath`

The virtual root of the mapping to remove.

#### Return value

Returns an `HRESULT`. `S_OK` indicates success. A failing HRESULT indicates that the
mapping could not be found or removed, or that the caller does not have the required
access.

#### Remarks

The mapping must be removed using the scope with which it was created. A global mapping
and a mapping at the same path in a silo are different mappings.

The job-container code in hcsshim does not expose this function in its internal Win32
wrapper. Its per-silo mappings are cleaned up as part of the job/silo lifetime rather
than by an explicit `BfRemoveMapping` call. The vendored go-winio package exposes the
function for global mappings and calls it with a zero job handle.

### BfGetMappings

Retrieves Bind Filter mappings into a caller-provided buffer.

#### Syntax

```cpp
HRESULT WINAPI BfGetMappings(
    _In_         ULONG   Flags,
    _In_opt_     HANDLE  JobHandle,
    _In_opt_     LPCWSTR VirtualizationRootPath,
    _In_opt_     PSID    Sid,
    _Inout_      PULONG  BufferSize,
    _Out_writes_bytes_to_(*BufferSize, *BufferSize)
                 PBYTE   OutBuffer
);
```

#### Parameters

`Flags`

Selects the mapping scope or query mode. See [BfGetMappings flags](#bfgetmappings-flags).

`JobHandle`

An optional job handle used when querying job- or silo-scoped mappings. Pass `NULL` or
`0` when the selected query mode does not require a job handle, such as the volume query
used by go-winio.

`VirtualizationRootPath`

An optional path used to select mappings. For a volume query, the path may be any
existing path on the volume. For example, a caller can use `C:\`, a volume GUID path,
or a child path below the volume.

`Sid`

An optional security identifier used by user-scoped queries. Pass `NULL` when the
selected query mode does not require a SID.

`BufferSize`

On input, the size of `OutBuffer` in bytes. On output, the number of bytes written or
the size indicated by the filter for the returned result.

The caller must inspect the returned size and validate every offset and length before
reading the buffer. The response is a binary variable-length format, not a public SDK
structure.

`OutBuffer`

The caller-allocated output buffer. The buffer begins with a response header followed by
mapping entries, target-entry arrays, and UTF-16 strings. See
[BfGetMappings buffer format](#bfgetmappings-buffer-format).

#### Return value

Returns an `HRESULT`. `S_OK` indicates that the output buffer contains a response. A
failing HRESULT indicates that the query could not be completed.

The response header also contains a filter status field. A caller should validate that
field and the buffer layout rather than relying only on the HRESULT.

#### Remarks

The go-winio implementation queries mappings by volume using
`BINDFLT_GET_MAPPINGS_FLAG_VOLUME`, passes a zero job handle, supplies a path on the
volume, and allocates a 256-KB buffer. That is an implementation choice, not a
documented maximum response size. A production caller should provide a resize/retry
strategy if the interface reports that the buffer is insufficient.

## BfSetupFilter flags

The following flag values are used by the code examined in hcsshim and go-winio.

| Name | Value | Description |
| --- | ---: | --- |
| `BINDFLT_FLAG_READ_ONLY_MAPPING` | `0x00000001` | Makes the mapping read-only. |
| `BINDFLT_FLAG_MERGED_BIND_MAPPING` | `0x00000002` | Merges an existing directory virtual root with the backing target. Backing entries take read precedence on name collisions. The behavior observed through `BfSetupFilter` matches the documented `CREATE_BIND_LINK_FLAG_MERGED` rules, with additional collision details recorded below. |
| `BINDFLT_FLAG_USE_CURRENT_SILO_MAPPING` | `0x00000004` | Uses the mapping namespace associated with the current silo. Used by hcsshim for per-container mappings. |
| `BINDFLT_FLAG_REPARSE_ON_FILES` (internal name) | `0x00000008` | Accepted by this build; exact create-path semantics are undocumented. |
| `BINDFLT_FLAG_SKIP_SHARING_CHECK` (internal name) | `0x00000010` | Rejected by direct single-mapping `BfSetupFilter` calls in the tested combinations. |
| `BINDFLT_FLAG_CLOUD_FILES_ECPS` (internal name) | `0x00000020` | Enables a Cloud Files ECP path; see the symbol-derived discussion below. |
| `BINDFLT_FLAG_NO_MULTIPLE_TARGETS` | `0x00000040` | Fails the mapping if it would produce multiple targets. Used by the vendored go-winio global-binding helper. |
| `BINDFLT_FLAG_IMMUTABLE_BACKING` (internal name) | `0x00000080` | Accepted; did not enforce immutability against an external backing rename. |
| `BINDFLT_FLAG_PREVENT_CASE_SENSITIVE_BINDING` (internal name) | `0x00000100` | Accepted with case-sensitive virtual and backing directories in the tested cases. |
| `BINDFLT_FLAG_EMPTY_VIRT_ROOT` (internal name) | `0x00000200` | Accepted with a nonempty virtual root; not a setup-time emptiness validator here. |
| `BINDFLT_FLAG_NO_REPARSE_ON_ROOT` (internal name) | `0x10000000` | Accepted; child lookup through a symlink root still mapped normally. |
| `BINDFLT_FLAG_BATCHED_REMOVE_MAPPINGS` (internal name) | `0x20000000` | Accepted by `BfSetupFilter`; no baseline behavioral difference was observed. |

The flags are bit values and can be combined. For example:

```cpp
ULONG flags = BINDFLT_FLAG_USE_CURRENT_SILO_MAPPING;
if (readOnly) {
    flags |= BINDFLT_FLAG_READ_ONLY_MAPPING;
}
```

`BINDFLT_FLAG_NO_MULTIPLE_TARGETS` is not defined in hcsshim's internal wrapper, but it
is defined and used by the vendored go-winio bind-filter package.

The additional names in the table are recovered internal names, not public SDK
constants. Their names are useful clues, but accepting a bit does not establish its
contract. On `10.0.26100.33158`, one-bit probes for `0x8`, `0x20`, `0x80`, `0x100`,
`0x200`, `0x10000000`, and `0x20000000` all created an ordinary writable mapping.
`0x10` returned `E_INVALIDARG`, both alone and combined with
`NO_MULTIPLE_TARGETS`.

Targeted controls narrowed what these bits do **not** promise:

* Holding the backing directory with sharing mode zero did not block baseline setup;
  `SKIP_SHARING_CHECK` remained invalid, so this test did not expose a sharing check
  for the flag to skip.
* `PREVENT_CASE_SENSITIVE_BINDING` accepted both a case-sensitive virtual root and a
  case-sensitive backing directory containing distinct `name.txt` and `NAME.txt`;
  both names remained distinct. It is not a general setup-time prohibition in this
  path.
* `EMPTY_VIRT_ROOT` accepted a virtual root containing a physical file. Like baseline
  non-merged mapping, the backing tree masked that file.
* With `IMMUTABLE_BACKING`, an external rename of the backing directory succeeded and
  the mapped path then failed with `ERROR_PATH_NOT_FOUND`, exactly like baseline. The
  bit expresses an optimization/assumption or downstream behavior, not an ACL-like
  immutability guarantee.
* `REPARSE_ON_FILES` and `NO_REPARSE_ON_ROOT` did not change child-file reads when the
  virtual root itself was a directory symlink. This does not test every reparse tag or
  root-handle operation.
* Creating a mapping whose virtual root was itself a directory symlink caused
  `BfGetMappings` to report the symlink's resolved physical directory as the virtual
  root. A root-mode PATH passthrough therefore cannot expose such an alias with a Bind
  Link alone: it must recreate the directory symlink in the isolated backing tree and
  map the resolved target separately.

Cloud Files ECP behavior requires a real Cloud Files placeholder/provider context;
ordinary NTFS files cannot exercise it. Batched-removal semantics likewise belong to
the batched configuration path and cannot be inferred from successful single-mapping
setup.

## Observed merged-bind behavior

This section distinguishes a merged bind from a copy-on-write overlay. It combines the
documented behavior of the public Bindlink API with a direct `BfSetupFilter` probe.
The results are qualified only for the tested system:

| Property | Value |
| --- | --- |
| Operating system | Windows Server 2025 Datacenter |
| Version | 10.0.26100.0 |
| Build | 26100 |
| Scope | Global mapping with a null job handle |
| Virtual and backing filesystems | NTFS directories on the same host volume |

The probe created an existing virtual directory containing one set of files and a
separate backing directory containing another set. It tested root and nested directory
enumeration, collisions, create, direct in-place open/write, replace-style write,
delete, and rename. Each mapping was removed before the physical directories were
inspected.

These observations are not a supported ABI guarantee for other Windows builds. Silo
scope, cross-volume roots, ReFS, volume targets, reparse points, hard links, alternate
data streams, memory-mapped files, and sharing violations require separate
qualification.

### Recursive namespace merge

With `BINDFLT_FLAG_MERGED_BIND_MAPPING`, directory entries from the existing virtual
root and the backing directory were both visible. Directories with the same name were
merged recursively. For example:

```text
virtual\common\virtual.txt
backing\common\backing.txt

merged view:
  common\virtual.txt
  common\backing.txt
```

When a file existed at the same relative path in both places, reads returned the
backing file. This target-over-virtual precedence also applied inside recursively
merged directories.

This behavior matches the public `CREATE_BIND_LINK_FLAG_MERGED` documentation: a
merged link combines the directory trees, the backing entry masks a colliding virtual
entry, and the merge is recursive.

### Writable merged mapping

With flags `0x00000002` (`MERGED_BIND_MAPPING` without `READ_ONLY_MAPPING`), mutations
were routed according to the object or destination selected by the mapping:

| Operation through the merged path | Observed physical result |
| --- | --- |
| Read a backing-only file | Read from the backing directory. |
| Read a virtual-only file | Read from the original virtual directory. |
| Read a collision | Read the backing file; the virtual file was masked. |
| Modify or directly open a backing-only file for write | Modified the backing file in place. No copy-up occurred. |
| Modify a virtual-only file | Modified the original virtual file. |
| Modify a collision | Modified the backing file. |
| Create a new file | Created the file in the backing directory. |
| Delete a backing-only file | Deleted the backing file. No whiteout was created. |
| Delete a virtual-only file | Deleted the virtual file. |
| Delete a collision | Deleted the backing file, after which the previously masked virtual file became visible. |
| Rename a backing-only file to a new name | Renamed it within the backing directory. |
| Rename a virtual-only file to a new name | Removed the source from the virtual directory and created the destination in the backing directory. |
| Rename a collision | Renamed the backing file; the original virtual file at the old name became visible. |

The destination behavior is particularly important: a new name belongs to the writable
backing target, so a rename can move a virtual-root file into the backing directory.

This mode is a writable merged namespace, not a safe lower/upper overlay. If the
backing directory is a live host tree, writes, deletes, and renames can change that host
tree immediately.

### Read-only merged mapping

With flags `0x00000003` (`MERGED_BIND_MAPPING | READ_ONLY_MAPPING`), the read-only
restriction applied to entries supplied by the backing path. The original virtual
directory remained writable:

| Operation through the merged path | Observed physical result |
| --- | --- |
| Read a backing-only file | Succeeded. |
| Modify, direct-open-for-write, delete, or rename a backing-only file | Failed; the tested APIs reported file-not-found-style errors. The backing file was unchanged. |
| Create a new file | Created the file in the original virtual directory. |
| Modify, delete, or rename a virtual-only file | Succeeded in the original virtual directory. |
| Read a collision | Returned the backing file. |
| Modify a collision when a physical virtual file also existed | Modified the masked virtual file; subsequent reads still returned the unchanged backing file. |
| Delete a collision when a physical virtual file also existed | Deleted the masked virtual file; the backing file remained visible and unchanged. |
| Rename a collision when a physical virtual file also existed | Renamed the masked virtual file; the backing file remained at the old name, while the renamed virtual file appeared at the new name. |

The collision result means read resolution and mutation resolution can select different
physical objects. A caller must not assume that a successful write followed by a read
of the same colliding path will return the bytes just written.

If a file existed only in the read-only backing, an attempted modification did not
copy it into the virtual directory. There was no automatic materialization, copy-up,
or tombstone creation.

This mode can provide a useful "writable virtual directory plus read-only injected
content" view, but it is not an OverlayFS-style upper layer because:

- backing-only files cannot be transparently modified;
- virtual files do not take read precedence over backing collisions;
- deletes do not create whiteouts;
- lower changes remain live and are resolved when a path is opened;
- collision writes can update a masked virtual object without changing what reads see.

### Non-merged mapping

With no merged flag, the backing directory shadowed the existing virtual directory.
Only backing entries were visible, including within same-name directories. New files
and ordinary mutations were applied to the backing directory. The existing physical
virtual contents were unchanged but inaccessible through the mapped path until the
mapping was removed.

### Repeated setup for one virtual root

The probe called `BfSetupFilter` a second time for an already mapped global virtual
root. The second call returned:

```text
E_INVALIDARG (0x80070057)
```

This occurred for:

- two default non-merged mappings;
- two merged mappings; and
- two mappings using `BINDFLT_FLAG_NO_MULTIPLE_TARGETS`.

Therefore, repeated setup must not be described as an append-target operation on the
tested build. The `NumberOfTargets` field in `BfGetMappings` and the
`NO_MULTIPLE_TARGETS` flag accommodate mappings that resolve to multiple targets
through mechanisms such as mapping composition; they do not establish that repeated
same-root setup is supported.

### Consequences for union and overlay designs

A merged bind avoids projected-file hydration: reads continue to access the selected
physical object, and no content is automatically copied merely because it was read.
That property avoids the clean-cache growth and staleness issues associated with a
projection system.

It also means the Bind Filter supplies none of the state transitions required for a
general copy-on-write union filesystem. In particular, it does not provide:

- copy-up when a read-only backing file is first opened for write;
- a persistent whiteout when a backing file is deleted from the logical view;
- upper-over-lower collision precedence;
- transactional cross-layer rename; or
- dirty-file and lower-version tracking.

Those semantics require another interception or virtualization layer, eager copying,
or explicit per-path mappings. A merged bind by itself should be documented as a
merged namespace rather than as CoW, UnionFS, or OverlayFS.

## BfGetMappings flags

| Name | Value | Description |
| --- | ---: | --- |
| `BINDFLT_GET_MAPPINGS_FLAG_VOLUME` | `0x00000001` | Queries mappings whose virtual root is associated with the selected volume/path. |
| `BINDFLT_GET_MAPPINGS_FLAG_SILO` | `0x00000002` | Selects silo-scoped mappings. The required combination of `JobHandle` and path is implementation-defined. |
| `BINDFLT_GET_MAPPINGS_FLAG_USER` | `0x00000004` | Selects user-scoped mappings. The `Sid` parameter identifies the user. |

The source code uses only `BINDFLT_GET_MAPPINGS_FLAG_VOLUME`:

```cpp
HRESULT hr = BfGetMappings(
    BINDFLT_GET_MAPPINGS_FLAG_VOLUME,
    nullptr,
    L"C:\\",
    nullptr,
    &bufferSize,
    buffer
);
```

## BfGetMappings buffer format

The following layout is inferred from the parsing code in
`vendor/github.com/Microsoft/go-winio/pkg/bindfilter/bind_filter.go`. It is not a
publicly documented ABI and should be treated as version-specific.

All integer fields are 32-bit unsigned values. UTF-16 string lengths and offsets are in
bytes. Offsets are relative to the beginning of `OutBuffer`.

### Response header

The first 12 bytes contain:

```cpp
struct BINDFLT_GET_MAPPINGS_RESPONSE_HEADER {
    ULONG Size;
    ULONG Status;
    ULONG MappingCount;
};
```

`Size`

Size of the response described by the buffer.

`Status`

Status supplied by the Bind Filter for the query. The exact status-code contract is not
documented; callers should not assume it is a Win32 error code.

`MappingCount`

Number of mapping entries following the header.

### Mapping entry

Each mapping entry is represented by five 32-bit fields:

```cpp
struct BINDFLT_MAPPING_ENTRY {
    ULONG VirtualRootLength;
    ULONG VirtualRootOffset;
    ULONG Flags;
    ULONG NumberOfTargets;
    ULONG TargetEntriesOffset;
};
```

`VirtualRootLength`

Length in bytes of the UTF-16 virtual-root string.

`VirtualRootOffset`

Offset in `OutBuffer` of the UTF-16 virtual-root string.

`Flags`

Flags associated with the mapping.

`NumberOfTargets`

Number of backing targets associated with this virtual root.

`TargetEntriesOffset`

Offset in `OutBuffer` of the first target entry.

### Target entry

Each target entry is represented by two 32-bit fields:

```cpp
struct BINDFLT_MAPPING_TARGET_ENTRY {
    ULONG TargetRootLength;
    ULONG TargetRootOffset;
};
```

`TargetRootLength`

Length in bytes of the UTF-16 target path.

`TargetRootOffset`

Offset in `OutBuffer` of the UTF-16 target path.

### Buffer validation

Because the response contains offsets and lengths supplied by the system component, a
caller must validate at least the following before dereferencing a field:

1. `BufferSize` is large enough for the response header.
2. `MappingCount` does not cause the mapping-entry array to exceed the buffer.
3. Every virtual-root offset and length falls inside the buffer.
4. Every target-entry offset and count falls inside the buffer.
5. Every target-string offset and length falls inside the buffer.
6. UTF-16 strings are properly terminated or are handled using their explicit byte
   lengths.

The go-winio parser additionally converts returned NT device paths such as
`\Device\HarddiskVolume2\...` to usable DOS or volume-GUID paths by opening the path
through `\\.\GLOBALROOT` and calling `GetFinalPathNameByHandle`.

## Mapping scope

The scope is selected primarily by the job handle and flags:

### Global mapping

Pass a zero job handle:

```cpp
BfSetupFilter(
    nullptr,
    BINDFLT_FLAG_NO_MULTIPLE_TARGETS,
    L"C:\\virtual",
    L"C:\\backing",
    nullptr,
    0
);
```

The vendored go-winio helper uses this mode. A global mapping is not isolated to one
container or silo and must be explicitly removed with:

```cpp
BfRemoveMapping(nullptr, L"C:\\virtual");
```

### Job/silo mapping

Pass the job handle and use the silo flag:

```cpp
BfSetupFilter(
    siloJob,
    BINDFLT_FLAG_USE_CURRENT_SILO_MAPPING,
    L"C:\\hpc",
    L"\\\\?\\Volume{GUID}\\",
    nullptr,
    0
);
```

The job must be promoted to a silo before the binding is created. hcsshim rejects the
operation locally when its job wrapper does not believe the job is a silo.

Processes assigned to the job see the mapping. Processes outside the job normally see
the ordinary host view instead. The hcsshim test
`internal/jobobject/jobobject_test.go` verifies this behavior by checking that the host
cannot stat the virtual path while a process assigned to the silo can access it.

## Silo-scoped bind links

A silo-scoped bind link combines two independent Windows mechanisms:

1. A job object is promoted to a silo with
   `SetInformationJobObject(JobObjectCreateSilo)`.
2. `BfSetupFilter` associates a mapping with that job by receiving the job handle and
   `BINDFLT_FLAG_USE_CURRENT_SILO_MAPPING`.

The result is a Bind Filter filesystem view that is specific to the job/silo. It is
similar to a process namespace, but it is not a Linux mount namespace and it is not
selected by an arbitrary PID or by the parent-child relationship alone. The relevant
condition is whether the accessing process belongs to the job/silo mapping scope.

### Silo-scoped mapping sequence

The normal sequence is:

```text
CreateJobObjectW or NtCreateJobObject
    |
    +-- SetInformationJobObject(
    |       JobObjectExtendedLimitInformation,
    |       JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
    |    )
    |
    +-- SetInformationJobObject(
    |       JobObjectCreateSilo,
    |       NULL,
    |       0
    |    )
    |
    +-- BfSetupFilter(
    |       JobHandle,
    |       BINDFLT_FLAG_USE_CURRENT_SILO_MAPPING,
    |       VirtualizationRootPath,
    |       VirtualizationTargetPath,
    |       ...
    |    )
    |
    +-- CreateProcessW with PROC_THREAD_ATTRIBUTE_JOB_LIST
        or AssignProcessToJobObject
```

The order matters. The job must be a silo before the silo-scoped Bind Filter mapping is
created, and the process must be placed in the job before it can use the mapping.

When a job is promoted to a silo, it must not already contain running processes. Create
and configure the job first, promote it while it is empty, and only then create or
assign the workload processes.

### Creating the job silo

The job can be created through either the Win32 or native NT entry point:

```cpp
HANDLE siloJob = CreateJobObjectW(
    nullptr,
    L"Global\\ExampleSilo");
```

or:

```cpp
NTSTATUS status = NtCreateJobObject(
    &siloJob,
    JOB_OBJECT_ALL_ACCESS,
    &objectAttributes);
```

Before promotion, configure the job to terminate its processes when its final handle is
closed:

```cpp
JOBOBJECT_EXTENDED_LIMIT_INFORMATION limits = {};
limits.BasicLimitInformation.LimitFlags =
    JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE;

BOOL ok = SetInformationJobObject(
    siloJob,
    JobObjectExtendedLimitInformation,
    &limits,
    sizeof(limits));
```

Then promote the empty job to a silo:

```cpp
BOOL ok = SetInformationJobObject(
    siloJob,
    JobObjectCreateSilo,
    nullptr,
    0);
```

`JobObjectCreateSilo` is the job-object information class used for promotion. There is
no separate public `CreateSilo` call in this path.

### Creating the silo-scoped bind link

Once `siloJob` is a silo, pass its handle to `BfSetupFilter` and include the silo flag:

```cpp
ULONG flags = BINDFLT_FLAG_USE_CURRENT_SILO_MAPPING;

HRESULT hr = BfSetupFilter(
    siloJob,
    flags,
    L"C:\\container\\data",       // virtual root inside the silo
    L"C:\\host\\data",            // backing path
    nullptr,
    0);
```

For a read-only mapping, combine the flags:

```cpp
ULONG flags =
    BINDFLT_FLAG_USE_CURRENT_SILO_MAPPING |
    BINDFLT_FLAG_READ_ONLY_MAPPING;
```

The `JobHandle` and the silo flag are both significant. Passing a job handle without
`BINDFLT_FLAG_USE_CURRENT_SILO_MAPPING` does not express the same operation as the
hcsshim silo path. Passing a null job handle creates the global form instead.

The virtual root's parent directory must exist before the call. The target can be a
file, directory, or volume supported by the installed Bind Filter implementation. An
existing virtual destination may be shadowed by the mapping.

### Assigning processes to the silo

The preferred process-creation form is to include the job in the process attribute list:

```cpp
SIZE_T attributeListSize = 0;
InitializeProcThreadAttributeList(
    nullptr,
    1,
    0,
    &attributeListSize);

std::vector<BYTE> storage(attributeListSize);
auto attributeList = reinterpret_cast<PPROC_THREAD_ATTRIBUTE_LIST>(
    storage.data());

InitializeProcThreadAttributeList(
    attributeList,
    1,
    0,
    &attributeListSize);

UpdateProcThreadAttribute(
    attributeList,
    0,
    PROC_THREAD_ATTRIBUTE_JOB_LIST,
    &siloJob,
    sizeof(siloJob),
    nullptr,
    nullptr);

STARTUPINFOEXW startupInfo = {};
startupInfo.StartupInfo.cb = sizeof(startupInfo);
startupInfo.lpAttributeList = attributeList;

CreateProcessW(
    nullptr,
    commandLine,
    nullptr,
    nullptr,
    FALSE,
    EXTENDED_STARTUPINFO_PRESENT,
    nullptr,
    nullptr,
    &startupInfo.StartupInfo,
    &processInformation);
```

An already-created process can instead be associated with the job using
`AssignProcessToJobObject`, subject to the normal Windows job restrictions.

Child processes normally remain associated with the job and therefore see the same
silo-scoped mapping. The scope is nevertheless job membership, not a guaranteed PID
tree. A process deliberately created outside the job, or a process that leaves through
permitted job-breakaway behavior, will not receive the job's mapping view.

### Visibility behavior

Given this mapping:

```text
Virtual root inside silo:  C:\container\data
Backing target:            C:\host\data
Silo job:                  ExampleSilo
```

the expected views are:

| Accessing process | `C:\container\data` |
| --- | --- |
| Process assigned to `ExampleSilo` | Resolves to `C:\host\data` through Bind Filter. |
| Normal host process | Sees the ordinary host view; it does not see the silo mapping. |
| Process in another silo | Sees that silo's mapping, if one exists; otherwise its ordinary view. |
| Child normally inherited into `ExampleSilo` | Resolves through the `ExampleSilo` mapping. |

The host path is not moved or copied. The mapping changes path resolution for processes
in the selected silo.

### Removing a silo-scoped bind link

Remove the mapping by passing the same job scope and virtual root:

```cpp
HRESULT hr = BfRemoveMapping(
    siloJob,
    L"C:\\container\\data");
```

The job handle must identify the same mapping scope used by `BfSetupFilter`. Passing a
null job handle to `BfRemoveMapping` addresses the global mapping scope, not the
silo-scoped mapping at the same path.

The hcsshim job-container implementation does not explicitly call `BfRemoveMapping`.
It closes the job and releases the layer resources; the silo-scoped mappings are
therefore tied to the job/silo lifetime. A tool that keeps a silo alive while changing
individual links, such as `bindutil-toolset`, can remove them explicitly.

### Querying silo mappings

Use `BfGetMappings` with the silo query flag and the job handle:

```cpp
ULONG bufferSize = 5 * 4096;
std::vector<BYTE> buffer(bufferSize);

HRESULT hr = BfGetMappings(
    BINDFLT_GET_MAPPINGS_FLAG_SILO,
    siloJob,
    nullptr,
    nullptr,
    &bufferSize,
    buffer.data());
```

The bindutil-toolset uses this form when a silo name is supplied. For a global query it
instead uses `BINDFLT_GET_MAPPINGS_FLAG_VOLUME`, a null job handle, and a path such as
`C:\\`. See [BfGetMappings buffer format](#bfgetmappings-buffer-format) for the
version-sensitive response layout.

### hcsshim implementation pattern

For HostProcess/job containers, hcsshim follows this pattern:

1. Create a named job object for the container.
2. Set `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`.
3. Promote the empty job with `JobObjectCreateSilo`.
4. Mount/union the container layers to obtain a backing volume path.
5. Call `BfSetupFilter` with the job handle and
   `BINDFLT_FLAG_USE_CURRENT_SILO_MAPPING` to expose the rootfs at a stable path such
   as `C:\\hpc`.
6. Call the same API for each requested OCI mount, mapping its source to its destination.
7. Launch processes with the job in `PROC_THREAD_ATTRIBUTE_JOB_LIST`.

The relevant hcsshim calls are implemented in
`internal/jobobject/jobobject.go` and `internal/jobcontainers/jobcontainer.go`.

This is different from an HCS-managed process-isolated/server container. In that path,
hcsshim calls `vmcompute.dll!HcsCreateComputeSystem` with the container configuration;
HCS creates and owns the server silo. hcsshim may later open the HCS-created job by name
for statistics, but it does not perform the server-silo promotion itself.

## Examples

### Dynamically resolve BfSetupFilter

Because `bindfltapi.dll` has no public SDK import library in the interface described by
this document, callers that use it commonly resolve the export dynamically.

```cpp
#include <windows.h>

using PFN_BfSetupFilter = HRESULT (WINAPI *)(
    HANDLE,
    ULONG,
    LPCWSTR,
    LPCWSTR,
    LPCWSTR*,
    ULONG);

int CreateSiloBinding(
    HANDLE siloJob,
    LPCWSTR virtualRoot,
    LPCWSTR backingPath,
    bool readOnly)
{
    HMODULE module = LoadLibraryW(L"bindfltapi.dll");
    if (module == nullptr) {
        return static_cast<int>(GetLastError());
    }

    auto setup = reinterpret_cast<PFN_BfSetupFilter>(
        GetProcAddress(module, "BfSetupFilter"));
    if (setup == nullptr) {
        DWORD error = GetLastError();
        FreeLibrary(module);
        return static_cast<int>(error);
    }

    ULONG flags = 0x00000004; // BINDFLT_FLAG_USE_CURRENT_SILO_MAPPING
    if (readOnly) {
        flags |= 0x00000001; // BINDFLT_FLAG_READ_ONLY_MAPPING
    }

    HRESULT hr = setup(
        siloJob,
        flags,
        virtualRoot,
        backingPath,
        nullptr,
        0);

    FreeLibrary(module);
    return SUCCEEDED(hr) ? 0 : static_cast<int>(hr);
}
```

In production code, keep the module loaded while any function pointer obtained from it
may be called. The example unloads the module immediately after the one call only
because no function pointer is retained.

### Remove a global mapping

```cpp
using PFN_BfRemoveMapping = HRESULT (WINAPI *)(HANDLE, LPCWSTR);

HMODULE module = LoadLibraryW(L"bindfltapi.dll");
auto removeMapping = reinterpret_cast<PFN_BfRemoveMapping>(
    GetProcAddress(module, "BfRemoveMapping"));

HRESULT hr = removeMapping(nullptr, L"C:\\virtual");

FreeLibrary(module);
```

For a silo-scoped mapping, pass the same job handle used to create the mapping. Do not
use a zero job handle to remove a mapping that was created in a silo scope.

## Requirements

| Requirement | Description |
| --- | --- |
| Operating system | A Windows installation that supplies a compatible `bindfltapi.dll` and Bind Filter implementation. |
| Driver | The `bindflt.sys` minifilter must be installed and available to the system. |
| DLL | `bindfltapi.dll`, normally in `%SystemRoot%\\System32`. |
| Privileges | The caller must satisfy the access checks imposed by the installed Windows version, the target path, and the selected job/silo scope. |
| Header | No public Windows SDK header is assumed for the `Bf*` declarations. |
| Library | No public `bindflt.lib` import library is assumed. Resolve the functions dynamically if using this interface. |

The existence of `bindfltapi.dll` alone does not guarantee that a particular call will
succeed. The driver, OS build, job/silo state, path form, and caller permissions all
affect the result.

## See also

- [Bindlink API overview](https://learn.microsoft.com/en-us/windows/win32/bindlink/)
- [CreateBindLink function](https://learn.microsoft.com/en-us/windows/win32/api/bindlink/nf-bindlink-createbindlink)
- [RemoveBindLink function](https://learn.microsoft.com/en-us/windows/win32/api/bindlink/nf-bindlink-removebindlink)
- [Bindlink API examples](https://learn.microsoft.com/en-us/windows/win32/bindlink/bindlink-example)
- [hcsshim `BfSetupFilter` declaration](https://github.com/microsoft/hcsshim/blob/main/internal/winapi/bindflt.go)
- [go-winio bind-filter implementation](https://github.com/microsoft/go-winio/blob/main/pkg/bindfilter/bind_filter.go)

## Research addendum (2026-08-07)

This addendum records source verification and experiments performed from WSL2 against
the Windows host. The original `C:\My\Projects\bindfltapi.md` was read-only for this
work and its SHA-256 remained
`30c35e7ee7d97a3c719cfab6c8faac5b41d8b1124792e593dd5512676316b779`.

### Evidence classification for every API usage

The three undocumented exports in the base article are real exports in the installed
`C:\Windows\System32\bindfltapi.dll` (Windows Server 2025, OS build 26100; file
version 26100.33158), but Microsoft
does not publish their declarations in the Windows SDK/Win32 reference. Each usage in
the article is nevertheless covered by at least one permitted evidence source:

| Usage | Microsoft Win32 documentation | Microsoft open-source usage | Classification |
| --- | --- | --- | --- |
| `LoadLibraryW`/`GetProcAddress` | Official Kernel32 API pages | `go-winio` generated lazy DLL/procedure bindings; hcsshim generated syscall binding | Public API, documented |
| `BfSetupFilter` | No public Win32 page found | `go-winio/pkg/bindfilter` and hcsshim `internal/winapi/bindflt.go` | Undocumented export, Microsoft-source corroborated |
| `BfRemoveMapping` | No public Win32 page found | `go-winio/pkg/bindfilter` and the copy vendored by hcsshim | Undocumented export, Microsoft-source corroborated |
| `BfGetMappings` | No public Win32 page found | `go-winio/pkg/bindfilter` and the copy vendored by hcsshim | Undocumented export, Microsoft-source corroborated |
| `CreateJobObjectW`, `OpenJobObject`, `AssignProcessToJobObject` | Official Job Objects pages | hcsshim `internal/jobobject` and job-container code | Public API, documented |
| `SetInformationJobObject(JobObjectExtendedLimitInformation)` | Official `SetInformationJobObject` page | hcsshim `SetTerminateOnLastHandleClose` | Public API, documented |
| `SetInformationJobObject(JobObjectCreateSilo)` | The information class is not fully described in the public SDK page | hcsshim `PromoteToSilo` and job-container tests | Undocumented/partially documented, Microsoft-source corroborated |
| `NtCreateJobObject`, `NtOpenJobObject`, `NtQueryInformationJobObject` | No supported Win32 contract; NT native interfaces are separately documented only in limited places | hcsshim `internal/winapi` uses them as an explicit NT variant | Native NT usage, source corroborated |
| `InitializeProcThreadAttributeList`, `UpdateProcThreadAttribute`, `CreateProcessW` with `PROC_THREAD_ATTRIBUTE_JOB_LIST` | Official Process Creation/Thread Attribute pages | hcsshim exec path and bindutil-toolset | Public API, documented |
| `GetFinalPathNameByHandle`, `\\.\GLOBALROOT` conversion | Official file/path API page; `GLOBALROOT` is an NT path convention | `go-winio` mapping parser | Public API plus implementation convention |

The flags and response structures in this article remain an **undocumented ABI**. The
public Microsoft Bindlink API documents only `CreateBindLink`, `RemoveBindLink`, and
`CREATE_BIND_LINK_FLAG_NONE`, `READ_ONLY`, and `MERGED`. It exposes no enumeration or
information-query function in SDK versions 10.0.26100.0 and 10.0.28000.0.

### Enumeration experiments

The probe created one global mapping and two silo mappings, then queried each scope.
The filter was loaded (`fltmc` showed `bindflt`, altitude 409800) and the calls ran
elevated on Windows Server 2025 build 26100.

* `BfGetMappings(BINDFLT_GET_MAPPINGS_FLAG_VOLUME, NULL, L"C:\\", ...)` returned
  the global mapping. Passing a child path on the same volume returned the same global
  mapping. Thus volume enumeration is volume/path-scoped, not a single undocumented
  “enumerate every volume” call.
* `BfGetMappings(BINDFLT_GET_MAPPINGS_FLAG_SILO, siloJob, NULL, ...)` returned the
  mapping for that silo. Two different silo handles returned different targets at the
  same virtual root.
* Passing a null job handle with the silo flag returned `E_INVALIDARG`.
* A user query with the current SID returned `S_OK`, a 12-byte response, and zero
  mappings. Passing a null SID failed with `0x800706F8` on this build. A live
  Microsoft Store MSIX process did not change that result.

**Conclusion:** the exposed enumeration is by volume/path, silo handle, or SID. No
supported or experimentally discovered call enumerates all global mappings across all
volumes or all silo mappings without first possessing the relevant volume path or silo
job handle. Enumerating named job objects is not equivalent: unnamed silos and access
checks prevent it from being a complete Bind Filter inventory.

### Nested jobs and nested silos

Two empty jobs were promoted with `SetInformationJobObject(JobObjectCreateSilo)`.
`BfSetupFilter` successfully created `C:\...\silo-root -> silo-a` in the first and
`C:\...\silo-root -> silo-b` in the second. A suspended process was assigned to both
jobs successfully. The same experiment with ordinary (non-silo) jobs also allowed the
second assignment on this host.

When the process read `silo-root\which.txt`, the mapping belonging to the **second
assignment** won. Reversing assignment order reversed the result. This is consistent
with an inner/nested job context overriding an outer context for the same virtual root;
it is not evidence that arbitrary job hierarchies are a stable public namespace ABI.
The experiment should be repeated on target Windows releases before relying on nested
silos operationally.

### Exact merged/union semantics

The public `CREATE_BIND_LINK_FLAG_MERGED` documentation says directory trees are merged
recursively, backing entries mask colliding virtual files, same-name directories merge,
and newly created files are placed in the backing path. Direct `BfSetupFilter` probes
on build 26100 matched those rules:

* Reads see both trees; backing wins collisions; nested directories merge recursively.
* Writable merged links modify backing-only entries in place and create new names in the
  backing tree. There is no copy-up, whiteout, or lower-version tracking.
* A collision can have different read and mutation objects. In particular, creating or
  truncating through a colliding name can change the backing object, while deleting it
  reveals the virtual object underneath. Renaming a virtual-only source to a new name
  materialized the destination in the backing tree on the tested build.
* Read-only merged links mask write permissions on backing entries only. The original
  virtual directory remains writable. A create/truncate-style open of a backing-only
  name can succeed by creating a virtual file, while direct modification of an existing
  backing-only object is denied; therefore “write” must always specify the Windows open
  disposition and collision state.
* A second `BfSetupFilter` for the same global virtual root returned `E_INVALIDARG` in
  non-merged, merged, and `NO_MULTIPLE_TARGETS` cases. `NumberOfTargets` is not proof
  that repeated same-root setup appends targets.

This is a merged namespace, not OverlayFS/UnionFS or a copy-on-write filesystem.

### MSIX and Bind Filter

Microsoft’s MSIX documentation describes package identity, package graphs, read-only
package files, dynamic VFS directory merges, and per-user/per-package AppData/registry
redirection. It does **not** document ordinary applications calling `BfSetupFilter`,
`BfRemoveMapping`, or `BfGetMappings` to implement MSIX package views. The installed
Microsoft Store package test likewise produced no user-scoped `BfGetMappings` entry.

Symbol analysis later in this article refines that negative result: package identity is
used specifically while Bind Filter constructs Cloud Files attribution ECPs. Therefore
the safe model is:

1. MSIX’s package graph and VFS/AppModel machinery provide the packaged-app view.
2. Bind Filter has a package-identity-aware Cloud Files attribution path. That narrow
   mechanism does not establish Bind Filter as the implementation of the general MSIX
   VFS/package-graph view.
3. A developer should not assume that creating a normal Bindlink/Bf mapping changes an
   MSIX package graph, package identity, package registration, or VFS precedence.
4. A Bind mapping can still be used by a packaged process if its path and security
   permissions allow access; that is separate from MSIX’s package virtualization.

### What a Job Silo adds beyond ordinary job limits

The source and hcsshim implementation show these practical additions:

* A silo-specific filesystem namespace, including per-silo Bind Filter mappings at the
  same virtual paths and a silo rootfs visible only to member processes.
* A queryable silo state (`JobObjectSiloBasicInformation` in hcsshim’s internal probe)
  and an explicit empty-job promotion step (`JobObjectCreateSilo`).
* Container-style lifecycle behavior: terminate-on-last-handle-close, process creation
  into the silo, inherited/breakaway job rules, and hcsshim process/token setup.
* The ordinary Job Object controls still apply: CPU/memory/IO limits, accounting,
  process-ID enumeration, IO completion-port notifications, termination, and security
  restrictions. hcsshim enables IO attribution and lifecycle notifications in addition
  to the Bind mapping.

The evidence does **not** justify claiming that a job silo alone supplies a complete
Linux-style PID, registry, network, or device namespace. HCS server silos and other
container subsystems provide additional isolation; hcsshim’s job-container path uses
tokens, layers, Bind Filter, and process-launch policy together.

A direct negative test reinforces that distinction. A process assigned to a basic job
silo created named events using both `Global\...` and `Local\...`; the host process
successfully opened both events while the silo process held them. Thus promotion with
`JobObjectCreateSilo` alone did not provide observable named-kernel-object isolation in
this test. Do not infer the richer Object Manager, registry, networking, hostname, or
service isolation of an HCS **server silo** from a basic job silo.

### Registry experiment: silo Bind Link versus Configuration Manager

The registry case was tested with disposable hives only. The probe created a small
`HKCU\\Software\\BindfltHiveSource` key, saved it as `virtual.hiv`, copied that file to
`backing.hiv`, and changed a value in the backing copy to `BACKING` while the virtual
copy contained `VIRTUAL`. It then installed a silo-scoped mapping:

```text
C:\Temp\...\hive\virtual.hiv -> C:\Temp\...\backing.hiv
```

A child was created suspended, assigned to a basic `JobObjectCreateSilo` job, and then
resumed. Inside that child, two independent operations produced:

```text
RegLoadAppKey(mapped-virtual-hive)                 -> BACKING
RegOpenKey(HKCU, "Software\\BindfltRegistryProbe") -> HOST
```

The first result proves that an explicit new hive-file load performs a filesystem open
that traverses the silo Bind Link. The second proves that ordinary predefined registry
handles are not selected by the file mapping: the child remained attached to the host
Configuration Manager namespace. A host-side control loaded the unmapped virtual hive
and observed `VIRTUAL`, confirming that the two hive files were genuinely different.

Therefore a Job Silo plus Bind Link can redirect an **explicit** `RegLoadAppKey` or
`RegLoadKey` operation when the hive is not already loaded, but it does not transparently
redirect `HKCU`, `HKLM`, or existing registry handles. Loading a hive does not make it
the predefined hive; a cooperative process must use the returned application-hive handle
or deliberately use a Registry-layer override such as `RegOverridePredefKey`.

Hive logs and transaction sidecars are separate sibling paths, so mapping only the base
file is not a coherent hive virtualization strategy. The safe layer distinction is:

```text
RegOpenKey(HKCU/HKLM) -> Configuration Manager's loaded-hive namespace
RegLoadAppKey(path)   -> new filesystem lookup -> Bind Filter may redirect
```

The experiment does not justify remapping active files under `System32\\Config` or a
logged-on user's `NTUSER.DAT`; that can create a split view between registry-managed
file objects and pathname-based tools, backups, recovery, or servicing.

### Public API coverage: go-winio versus hcsshim

| Capability | `go-winio/pkg/bindfilter` public package | hcsshim public API |
| --- | --- | --- |
| Global create | `ApplyFileBinding(root, source, readOnly)`; sets `NO_MULTIPLE_TARGETS` and optional `READ_ONLY` | Not public; only `internal/jobobject.JobObject.ApplyFileBinding` |
| Global remove | `RemoveFileBinding(root)` | Not public; hcsshim relies on job/silo lifetime for silo links |
| Global volume enumeration | `GetBindMappings(volumePath)`; parses the undocumented response | Not public |
| Silo create | Not exposed | Internal `JobObject.ApplyFileBinding`; requires `Options.Silo`/`PromoteToSilo` |
| Silo remove/query | Not exposed | No public Bind Filter remove/query wrapper; internal job handle and lifetime management |
| Merged flag | Constant is not exported by the current go-winio helper; helper intentionally uses `NO_MULTIPLE_TARGETS` | Internal wrapper defines the merged constant but the job-container path uses current-silo plus read-only, not merged |
| User-scoped query | Not exposed | Not exposed |
| Raw Filter port / undocumented protocol | Not exposed | Not exposed in the public package; only external research code `bindutil-toolset` implements it |

`hcsshim` is a container runtime library with an internal Bind Filter integration, not
a public Bind Filter SDK. External Go callers can use `go-winio` for the three global
operations, but cannot import hcsshim’s `internal` packages under Go’s import rules.

### Test environment and evidence record

The experiments were performed on Windows Server 2025, build 26100, with
`bindfltapi.dll` and `bindflt.sys` version `10.0.26100.33158`, an elevated native
Win32 test environment, and the `bindflt` minifilter loaded at altitude `409800`.
Matching Microsoft symbols were used for the installed user-mode and kernel binaries.
Disposable mappings, jobs, processes, hives, and directories were removed after each
test. Results are qualified for this build and should not be treated as a stable
third-party ABI contract.

## Mechanism-level correction and extended experiments

The earlier nested-silo experiment tested only collision precedence at one path. That
was insufficient to determine whether Bind Filter inherits or unions mappings across a
nested Job Silo hierarchy. The following experiments and symbol analysis supersede that
limited interpretation.

### Symbol-derived internal model

Matching Microsoft public symbols were downloaded for the installed build:

* `BindFltApi.pdb` GUID/age `B8DB86CD7C22E52F08E78ECE65F545F6/1`;
* `bindflt.pdb` GUID/age `9BBE10DBDFC590275881FE8DE2233419/1`.

`bindfltapi.dll` exports thirteen functions on this build, not merely the three wrappers
described in the base article: `BfAttachFilter`, `BfConfigureFilter`,
`BfGenerateBatchedConfig`, `BfGenerateMappingConfiguration`, `BfGetMappings`,
`BfRemoveMapping`, `BfRemoveMappingEx`, `BfSetupFilter`, `BfSetupFilterBatched`,
`BfSetupFilterEx`, `BfTrackWritesFromSilo`, `CreateBindLink`, and `RemoveBindLink`.

Relevant driver symbols include `BfMappingLookup`, `BfGetMappingContexts`,
`BfpGetInstanceTierNode`, `BfpCreateInstanceTierNode`, `BfWalkContainerRootId`,
`BfMergeEnumerationContexts`, `BfGetOrCreateSiloContext`, and imports for
`PsGetCurrentSilo`, `PsGetJobSilo`, `PsGetParentSilo`, and `PsGetHostSilo`. This is
direct evidence that the implementation has distinct instance/silo mapping tiers and
knows the kernel silo hierarchy; it does not by itself imply that every ancestor tier
participates in path lookup.

### Actual nested-silo lookup rule

The decisive experiment used **disjoint** roots:

```text
outer silo A:  outer-root -> outer-target
inner silo B:  inner-root -> inner-target
```

An outer-only process saw only `outer-root`; an inner-only process saw only
`inner-root`. A process assigned first to A and then to B saw `inner-root` but **did not
see `outer-root`**. An empty inner silo also hid the outer silo mapping, proving that
absence in the inner tier does not fall back to an ancestor silo tier.

A second experiment mapped an outer parent and an inner child:

```text
outer A: overlap-root     -> overlap-outer
inner B: overlap-root\sub -> overlap-inner
```

The nested process saw the inner child mapping but neither the outer root entry nor an
outer entry below `sub`. Therefore nested Job Silos are real Job Object membership and
hierarchy, but their Bind Filter mapping lists are **not inherited or unioned**.

The experimentally supported lookup rule for build 26100 is:

```text
global/host tier + caller SID tier + current (innermost) silo tier
```

Ancestor silo tiers are skipped. When the current silo contains a mapping at the same
root as a global mapping, the current-silo mapping wins. Global mappings remain visible
inside nested silos when not replaced; ancestor-silo mappings do not.

### Undocumented SID and combined silo+SID tiers

`BfSetupFilterEx(flags, job, sid, root, target, exceptions, count)` and
`BfRemoveMappingEx(job, sid, root)` were called directly. Three mappings were installed
at the same virtual root:

```text
global mapping                 -> GLOBAL
current-user SID mapping       -> USER
silo + current-user SID mapping -> SILO-USER
```

All calls returned `S_OK`. A normal host process resolved `USER`; a process in the silo
resolved `SILO-USER`. `BfGetMappings(VOLUME)`, `BfGetMappings(USER, sid)`, and
`BfGetMappings(SILO, job)` enumerated the three indices separately. Thus the `Sid`
argument is not merely query metadata: it identifies an active per-user mapping tier,
and a mapping may be keyed by both silo and SID.

### How merged mappings produce multiple targets

With `MERGED` on the user and silo+user mappings, `BfGetMappings` returned two targets
for the active silo mapping:

```text
target 0: the supplied silo backing path
target 1: the physical virtual-root path
```

This directly explains `NumberOfTargets > 1`: a merged mapping stores the backing path
plus a virtual-root fallback. Resolution through that fallback can encounter eligible
lower tiers. In the test, disjoint content from the global mapping remained reachable
from the silo merged view, but disjoint content from the skipped user mapping did not.
The implementation is therefore not a flat union of all mappings. It is a tiered lookup
in which a merged mapping explicitly re-enters resolution through its virtual-root
target, subject to the active tier-selection rules.

### Other undocumented mechanisms found

The filter-port protocol contains batched store/remove commands and command 10,
`BFC_TRACK_SILO_WRITES`. The DLL export constructs that request, while driver symbols
show `BfProcessTrackWritesRequest`, `BfpSetMappingNodeForTracking`,
`BfSetWriteTrackingEa`, and the literal NTFS extended-attribute name
`$BINDFLT.WRITTEN.IN.SILO`. This is strong evidence of an internal facility that marks
files written through a tracked silo mapping.

Disassembly recovers more of its contract. `BfTrackWritesFromSilo` serializes a
variable-length path at message offset `0x20`, stores its byte length in the descriptor
at `+0x20/+0x24`, stores the third API argument at message `+0x10`, and copies the
fourth argument as the eight-byte descriptor at `+0x18`. The driver:

1. rejects non-host-silo callers;
2. requires at least a 32-byte command payload;
3. references `+0x10` with `ObReferenceObjectByHandle(..., *PsJobType, ...)` and calls
   `PsGetJobSilo` on the resulting job;
4. validates the offset/length descriptor at `+0x18`, normalizes its embedded path,
   looks up the mapping node, sets node flag bit 28, and stores a tracking context at
   node offset `+0x38` through `BfpSetMappingNodeForTracking`.

Thus the third argument is a job handle and the fourth is a packed internal path
descriptor, not a SID, token, pointer, or arbitrary context integer. A null descriptor
returned `ERROR_INVALID_USER_BUFFER`; structurally plausible calls against a basic
`JobObjectCreateSilo` job reached the job/silo validation path but returned
`ERROR_INVALID_HANDLE`. Writes made afterward created no
`$BINDFLT.WRITTEN.IN.SILO` EA (`STATUS_NO_EAS_ON_FILE`). The remaining prerequisite
appears to be richer silo/mapping state than this basic promoted job provides. No
working or supported public prototype is claimed.

The package-identity connection is narrower than “Bind Filter implements MSIX VFS.”
The driver imports `RtlQueryPackageIdentity` and publishes `BfQueryPackageIdentity`,
but its call chain is under `BfAttachAttributionInformationEcp` and
`BfAddCloudFileECPs`, alongside `GUID_ECP_CLOUDFILES_ATTRIBUTION`. This is direct
evidence that package identity participates in Cloud Files attribution ECP creation.
It is **not** evidence that ordinary MSIX VFS construction is represented by
`BfSetupFilter` mappings. A narrow ETW capture around Microsoft Store and packaged
Notepad activation produced no BindFlt operational events, and the Store experiment
returned zero user-scoped mappings. The manifest provider exposes registration,
attach, and unload events rather than ordinary path activity, so ETW silence is not
proof of non-use; it merely provides no additional MSIX mapping evidence.

## Aliases and path spelling experiments

### One backing directory at multiple virtual paths

The same physical backing directory was bound simultaneously at `alias-a` and
`alias-b`, both as global writable mappings. Both links independently exposed the same
initial file. A file created through `alias-a` immediately appeared through `alias-b`
and in the physical backing directory, proving that Bind Filter does not clone or
snapshot the target.

Deleting the backing directory through `alias-a` made **both** aliases fail with
directory-not-found errors. Recreating the backing directory and a new file revived the
file through both aliases without recreating either mapping. This confirms that links
are path-based live references resolved at open time.

Read-only policy is attached to each mapping, not to the shared backing object. A
read-only alias and a writable alias were created for the same backing directory. A
write through the read-only alias failed with `ERROR_ACCESS_DENIED`; the same write
through the writable alias succeeded, and the changed content was then visible through
the read-only alias. Read-only Bind Links therefore do not make the target immutable or
coordinate policy across aliases.

Removing `alias-a` did not remove or modify `alias-b`; each virtual root is a separate
mapping entry even when the normalized target is identical.

### Canonical-equivalent virtual roots

After creating a link at a DOS path, a second link at the same path expressed as each
of the following returned `E_INVALIDARG` and did not replace the first mapping:

```text
\\?\C:\...       extended DOS
\\.\C:\...       Win32 device spelling
\??\C:\...       NT DOS object-manager spelling
\\?\GLOBALROOT\Device\HarddiskVolume3\...
\\?\Volume{GUID}\...
dot-segment path, trailing-slash path, and lower-case path
```

The duplicate check therefore operates on normalized path identity, not the original
string spelling. There is no alias-based bypass of the one-mapping-per-virtual-root
rule on the tested build.

### Accepted and rejected path forms

Both the undocumented `BfSetupFilter` wrapper and the public `CreateBindLink` export
accepted these forms for both virtual and backing paths:

* ordinary absolute DOS paths such as `C:\Temp\...`;
* `\\?\C:\...` and `\\.\C:\...`;
* `\??\C:\...`;
* `\\?\GLOBALROOT\Device\HarddiskVolume3\...`;
* volume-GUID paths such as `\\?\Volume{GUID}\...`;
* case variants, dot-segment paths, and a trailing separator.

The APIs canonicalized accepted forms to the same `\Device\HarddiskVolume3\...`
paths in `BfGetMappings`, and removing the mapping through the ordinary canonical DOS
path succeeded regardless of the spelling used at setup. Direct raw device paths of
the form `\Device\HarddiskVolume3\...` failed with `ERROR_PATH_NOT_FOUND`; relative
paths failed with `ERROR_FILE_NOT_FOUND`. `\\?\GLOBALROOT\Device\...` is accepted
because it is a Win32 extended path that explicitly bridges to the NT device namespace;
the bare NT device string is not accepted by the user-mode API path parser.

The supported `CreateBindLink` export and lower-level `BfSetupFilter` path parser showed
the same acceptance matrix on build 26100. Thus unusual spellings affect effectiveness
only when they cross the parser's absolute-path boundary; accepted spellings do not
create distinct Bind Link identities or distinct lookup behavior.

### Escape-hatch experiment: path aliases versus object aliases

The path-spelling matrix should not be overinterpreted as proving that Bind Links have
no escape hatches. It proves only that alternate names for the **same path** do not evade
the filter. A second experiment distinguished path aliases from aliases to the physical
file object:

* A directory symlink to the virtual root still resolved through the Bind Link and read
  the backing file.
* `\\?\`, `\\.\`, `\??\`, `GLOBALROOT`, volume-GUID, dot-segment, case-variant,
  short-name, trailing-component, and current-directory-relative access all read the
  backing file.
* Opening the virtual file with `FILE_FLAG_OPEN_REPARSE_POINT` did not bypass the link
  on this build; it still returned the backing content.
* A hard link created **before** the Bind Link, pointing at the physical virtual file,
  bypassed the mapping. Opening that hard-link name returned the original
  `PHYSICAL-VIRTUAL` content while the mapped virtual path returned `BACKING`. The
  filter is path-based; it does not retroactively rewrite every directory entry that
  resolves to the same file ID.
* A file handle opened on the physical virtual path **before** link creation continued
  to read `PHYSICAL-VIRTUAL` after the link was installed. Existing handles are not
  retargeted; callers must reopen paths to observe a newly created mapping.
* A corrected `OpenFileById` test used a 24-byte `FILE_ID_DESCRIPTOR` and a
  directory-capable `\\.\\C:` volume handle. It opened the pre-mapping physical file by
  NTFS file ID and returned `PHYSICAL-VIRTUAL`, while every pathname form at the mapped
  root returned `BACKING`. File-ID opens can therefore bypass pathname mapping on this
  NTFS/build combination.

Consequently, the precise security statement is:

> Bind Filter closes ordinary path-spelling and symlink/reparse-name alternatives, but
> it is not an object-identity namespace. Pre-existing hard-link names and handles
> opened before mapping creation can continue to reach the underlying physical object.

Applications requiring complete isolation must control hard-link creation, close or
invalidate pre-existing handles, restrict file-ID access where relevant, and avoid
treating a Bind Link as equivalent to a volume-wide object namespace.

### Filesystem edge cases (build 26100)

Focused NTFS controls produced the following observations:

| Operation | Result |
| --- | --- |
| Alternate data stream (`same.txt:meta`) | Selected the backing stream (`B-ADS`), just as the unnamed stream selected `BACKING`. |
| Memory map opened before setup | Continued to expose `VIRTUAL`; existing section/file objects were not retargeted. |
| Memory map opened after setup | Exposed `BACKING`; the new open traversed the mapping. |
| File-to-file mapping | Succeeded; reads and writes went directly to the target file (`TF` -> `CHANGED`). |
| Pre-existing hard link and file-ID open | Continued to expose the physical virtual file. |

The file-ID result strengthens the object-identity model: Bind Filter participates in
name resolution, but it does not virtualize all ways of acquiring an NTFS file object.
These tests do not establish behavior for oplocks, section-creation races, ReFS,
remote filesystems, TxF, or cross-volume mappings.
