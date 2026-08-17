## Build

The code is Windows-only (`GOOS=windows`). The native Windows build entry point
is the PowerShell script:

```powershell
Set-Location C:\My\Projects\bindmount
.\scripts\build.ps1
.\scripts\build.ps1 -Clean
.\scripts\build.ps1 -Test
.\scripts\build.ps1 -Vet
.\scripts\build.ps1 -Release
```

The script builds `dist\bindmount.exe` and `dist\decoy.exe`, copies the GUI
script, and uses `Compress-Archive` for releases. It requires only Go and
PowerShell.

From WSL or another Unix-like environment, the Makefile remains available:

```sh
make build            # regenerates both executables and dist/bindmount-gui.ps1
make release          # creates release/bindmount-windows-amd64.zip
```

or directly:

```sh
GOOS=windows GOARCH=amd64 go build -o dist/bindmount.exe ./cmd/bindmount
GOOS=windows GOARCH=amd64 go build -o dist/decoy.exe ./cmd/decoy
cp scripts/bindmount-gui.ps1 dist/bindmount-gui.ps1
```

Run the GUI from PowerShell:

```powershell
.\dist\bindmount-gui.ps1
```

Cross-compiling from Linux/WSL works because all dependencies are pure Go and
the project does not use cgo.

## CLI usage

```text
bindmount add [--silo <job>] <virtual-root> <target>
bindmount add [--silo <job>] <root[+][=|==]target>
bindmount remove [--silo <job>] <virtual-root>
bindmount list [--silo <job>] [<volume-path>]
bindmount exec [--detach] [--verbose] [--no-ui-restrictions] [--root data-dir | --readonly-root] [--passthrough name|--no-passthrough name]... [--link root[+][=|==]target]... <job-name> -- <command> [args...]
bindmount silo exists <job-name>
bindmount silo kill <job-name>
```

`--root` creates a backing directory for every drive visible when the silo is
created. It is a launch-time snapshot, not a drive hot-plug monitor. When
`--root` is supplied, executable, PATH, current-directory, Git-root, and
app-execution-alias passthrough are enabled by default:

- `executable` — expose the executable's containing directory.
- `path` — expose every existing directory listed in `PATH`.
- `cwd` — expose the launcher's current working directory.
- `gitroot` — expose the Git repository root containing the current directory.
- `appexec` — expose Windows app execution aliases, their package install
  roots, and existing per-user package state directories.

Each passthrough is read-write and can be independently controlled with
`--passthrough <name>` or `--no-passthrough <name>`. Without `--root`, all
passthrough types are disabled unless explicitly enabled. Two additional
passthrough types are always opt-in, even with `--root`, because they expose
persistent user state rather than launch-time dependencies:

- `appstate` — expose `APPDATA`, `LOCALAPPDATA`, and `C:\ProgramData`.
- `powershell` — expose the PSReadLine command history file and the per-user
  PowerShell module cache, and pass through `%TEMP%` so WLDP script trust works
  (without `%TEMP%`, PowerShell enters Constrained Language Mode inside the silo).

In `--root` mode, each drive initially resolves through its isolated backing
tree. `bindmount` creates the current `%USERPROFILE%` path in that tree, but it
does not populate the profile or automatically create the ancestors of later
links. A mapping for a child such as `%APPDATA%\ZCode` does **not** make
`%APPDATA%` itself resolvable. Applications may first resolve or validate a
parent directory before accessing their more specific state directory; Electron's
`app.getPath("appData")` is one example.

Use a parent mapping as a namespace anchor when the parent must exist without
exposing all of its host contents. The target can be any existing empty
directory. Install the more-specific child mapping before the parent anchor so
the child retains its own target. For example, these mappings are sufficient for
the tested ZCode installation while keeping the rest of `%APPDATA%` isolated:

```powershell
New-Item -ItemType Directory -Force C:\Empty | Out-Null

bindmount exec --root "$env:LOCALAPPDATA\bindmount\roots" `
  --no-ui-restrictions `
  --link "$env:USERPROFILE\.zcode=$env:USERPROFILE\.zcode" `
  --link "C:\Program Files=C:\Program Files" `
  --link "$env:APPDATA\ZCode=$env:APPDATA\ZCode" `
  --link "$env:APPDATA=C:\Empty" `
  z -- "C:\Program Files\ZCode\ZCode.exe"
```

Here `%APPDATA%=C:\Empty` makes `%APPDATA%` resolvable but presents an empty
directory, while the more-specific `%APPDATA%\ZCode` mapping exposes only
ZCode's state. A child link being resolvable must not be taken to mean that its
parent is also resolvable.

`--readonly-root` is mutually exclusive with `--root`. Instead of a backing
tree it maps every currently visible drive onto the same location with a
read-only bind link, so the silo sees the real drive contents but cannot
write to them. Unlike `--root`, it does not change the passthrough defaults.

`--no-ui-restrictions` leaves `JobObjectBasicUIRestrictions` unset on the silo.
Use it for applications such as Electron whose Chromium sandbox creates nested
Job Objects; Windows cannot form a nested Job hierarchy when a participating
job has UI restrictions.

### Process mitigations and Job limits

`exec` configures the following Job Object limits by default:

- `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE` terminates the silo's remaining
  processes when its last Job Object handle is closed.
- `JOB_OBJECT_LIMIT_BREAKAWAY_OK` and
  `JOB_OBJECT_LIMIT_SILENT_BREAKAWAY_OK` are deliberately not enabled. A child
  cannot escape the silo by requesting `CREATE_BREAKAWAY_FROM_JOB`, and child
  processes remain in the same bind-link view.
- `JobObjectBasicUIRestrictions` enables
  `JOB_OBJECT_UILIMIT_SYSTEMPARAMETERS`,
  `JOB_OBJECT_UILIMIT_DISPLAYSETTINGS`, `JOB_OBJECT_UILIMIT_DESKTOP`, and
  `JOB_OBJECT_UILIMIT_EXITWINDOWS`. Silo processes therefore cannot change
  system or display settings, create or switch desktops, or initiate system
  shutdown through the affected APIs.

The `--no-ui-restrictions` option disables the four UI restrictions as a group.
It does not change kill-on-close or the breakaway policy. No CPU, memory,
active-process-count, execution-time, or I/O-rate limit is currently configured.

At process creation, `bindmount` also requests these mitigation policies for
the process it launches directly:

- `PROCESS_CREATION_MITIGATION_POLICY_IMAGE_LOAD_NO_REMOTE_ALWAYS_ON` blocks
  executable images and DLLs from remote locations such as UNC paths.
- `PROCESS_CREATION_MITIGATION_POLICY_EXTENSION_POINT_DISABLE_ALWAYS_ON`
  disables legacy extension-point DLL injection mechanisms, including
  `SetWindowsHookEx` and `AppInit_DLLs`.

Applying the process mitigation policy is best-effort: if Windows rejects the
process attribute, `bindmount` prints a warning and continues. There is
currently no CLI option that disables these process mitigation policies.

`gitroot` uses `git rev-parse --show-toplevel`. If Git is not installed in
`PATH`, or the current directory is not inside a Git repository, no Git-root
mapping is created.

The `exec` command requires `--` before the child command. Options before `--`
belong to `bindmount`; everything after it is passed to the launched process.

## GUI usage

Just run the GUI helper script by yourself. It doesn't need Electron/WebView/CEF.
It won't install a bunch of runtimes or consume a lot of RAM on your machine.

The GUI uses `exec --detach` automatically. In detached mode, `bindmount.exe`
creates the silo and launches the requested command, then exits; the command
inherits the Job Object handle and therefore keeps the silo alive. A descendant
keeps the silo alive only if its creator also enables handle inheritance. If the
direct command exits without passing the handle on, Windows closes the last job
handle and terminates the remaining silo processes.

The GUI does not currently expose `--no-ui-restrictions`. To run an application
that creates nested Job Objects, copy or construct the command and run it from a
terminal with that option. The GUI's **Block WSL** option maps host `wsl.exe`
files to `decoy.exe`; therefore `decoy.exe` must remain next to
`bindmount.exe` in the generated package.

### Global mappings

Global mappings are visible to every process on the host:

```powershell
# Run elevated
mkdir C:\virtual
bindmount add C:\virtual C:\backing
dir C:\virtual                      # shows C:\backing contents
bindmount list C:\                  # lists the mapping
bindmount remove C:\virtual         # removes it
```

Mapping syntax:

- A `+` prefix makes a mapping merged: the existing virtual-root directory is
  recursively merged with the target; the target wins on name collisions. This is a merged namespace,
  **not** an OverlayFS/CoW layer: writes and deletes act directly on the
  backing tree, no copy-up or whiteouts. See
  [docs/BindFilterAPI.md](docs/BindFilterAPI.md#observed-merged-bind-behavior).

### Silo-scoped mappings

A silo-scoped mapping is visible only to processes inside a job silo. The
typical flow is `exec`, which creates the job, promotes it to a silo, creates
the links, and launches a command inside:

```powershell
# Run elevated. Creates the silo, two links, and runs a shell inside.
bindmount exec --link C:\app\data=D:\shared\data --link C:\app\cfg==D:\cfg-ro mysilo -- cmd.exe
```

Inside that `cmd.exe`, `C:\app\data` resolves to `D:\shared\data` and
`C:\app\cfg` is a read-only view of `D:\cfg-ro`. Host processes see the
ordinary filesystem at those paths. When the shell exits, the job is
terminated and its silo-scoped mappings disappear with it.

The other commands accept `--silo <job-name>` to operate on an existing
job's mappings (the job must already be a silo):

```powershell
bindmount list --silo mysilo
bindmount add --silo mysilo C:\more D:\more-backing
bindmount remove --silo mysilo C:\more
```

Read-only and merged modes are part of each mapping specification. Use one
equals sign for a writable mapping, two equals signs for a read-only mapping,
and prefix the separator with `+` for a merged mapping:

```powershell
bindmount add C:\Windows=C:\Windows
bindmount add C:\Windows==C:\Windows
bindmount add C:\data+=D:\shared
bindmount add C:\config+==D:\cfg
bindmount exec --link C:\data=D:\shared --link C:\config+==D:\cfg mysilo -- cmd.exe
```

The old standalone `--read-only` and `--merged` link modifiers are not
accepted. This avoids accidentally applying one mount's settings to a
different mount.

> **Note on `exec`:** on the tested build (26100), `CreateProcess` with
> `PROC_THREAD_ATTRIBUTE_JOB_LIST` fails with `ERROR_INVALID_PARAMETER` when
> the attribute list references a silo job, so `exec` automatically falls back
> to creating the process suspended and calling `AssignProcessToJobObject`.

### Scope and lifetime semantics (from the research document)

- A mapping must be removed with the same scope it was created with; a global
  mapping and a silo mapping at the same path are different mappings.
- Silo mappings are tied to the silo's lifetime — closing the last job handle
  (with kill-on-close) destroys them. Global mappings persist until removed.
- A second `add` for the same virtual root in the same scope fails with
  `E_INVALIDARG` (tested on build 26100); repeated setup does **not** append
  targets.
- Existing handles and pre-existing hard links are **not** retargeted by a new
  mapping; the filter is path-based, not object-identity-based.
- Mappings below a nested silo do not inherit ancestor-silo mappings; the
  lookup tiers are: global + caller-SID + innermost silo.

## Architecture

```text
cmd/bindmount/          CLI (add/remove/list/exec)
cmd/decoy/              Small executable used by the GUI's Block WSL option
scripts/                PowerShell WinForms GUI source
dist/                   Generated local Windows package
release/                Generated release archives
internal/bindfilter/    Wrapper: Options, scopes, grow-and-retry enumeration,
                        NT->DOS path conversion
internal/winapi/        Low-level bindings: dynamic bindfltapi.dll resolution,
                        checked BfGetMappings response parser, job-object and
                        process-attribute-list helpers
docs/BindFilterAPI.md   Research document: ABI, flags, observed behavior
```

Design decisions worth knowing:

- **Dynamic resolution.** `bindfltapi.dll` is loaded with an explicit
  `%SystemRoot%\System32` path and `GetProcAddress` (no import library
  exists). Missing DLL/exports produce `ErrBindfltUnavailable`.
- **Defensive parsing.** Every offset/length in the `BfGetMappings` response
  is bounds-checked before dereference, per the validation checklist in the
  research document; a nonzero filter status is surfaced as an error.
- **Enumeration with retry.** `BfGetMappings` starts at 64 KB and grows up to
  4x when the filter reports insufficient buffer (go-winio uses a fixed
  256 KB; the document recommends a resize strategy instead).
- **Path conversion.** Targets come back as NT device paths
  (`\Device\HarddiskVolumeN\...`); they're converted via
  `\\.\GLOBALROOT` + `GetFinalPathNameByHandle`, as go-winio does. A target
  that fails conversion is printed raw rather than dropped.
- **No state file.** The tool is stateless; the filter is queried live. This
  means `bindmount` cannot list silo mappings without the silo's job name —
  that is a limitation of the interface itself (documented in the research:
  no call enumerates "all silos").

## Testing

Unit tests cover the offline-checkable parts, including response parsing,
argument handling, path normalization, Job Object helpers, and App Execution
Alias parsing. The Windows test suite also contains a silo-scoped Bind Link
integration test, which skips when the required driver, OS support, or
privileges are unavailable. Run it with:

```powershell
go test ./...
```

From WSL, `make test` cross-compiles the test binaries without executing them.

End-to-end behavior (actually creating mappings) requires an elevated Windows
host with `bindflt` loaded; see docs/BindFilterAPI.md for the environment the
ABI details were verified against.
