# bindmount

`bindmount` manages Windows Bind Links (global or silo-scoped) through the
Bind Filter (`bindflt.sys`), as alternatives to Linux bind mounts and mount
namespaces on Windows. It provides:

- `bindmount.exe` — a CLI tool to deal with bind links;
- `bindmount-gui.ps1`** — a WinForms helper script around the CLI tool.

It is designed to be simple, stateless, daemonless, and easy to use.

**WARNING**: This project uses several internal or undocumented Windows
mechanisms and APIs. They are already used by Microsoft in its open-source
projects (go-winio, hcsshim, mxc, ...), so they are relatively stable.

> **Research note:** [`docs/BindFilterAPI.md`](docs/BindFilterAPI.md) is an
> evolving, project-wide research and implementation document. It may change
> as Windows builds and experiments provide new evidence; it is not a static
> source of truth or a supported Microsoft API document. The implementation
> validates all data returned by the undocumented interface and prefers the
> public Bindlink API where practical.

## Requirements

- Windows 10+ / Windows Server 2022+
- `bindflt.sys` loaded (see `fltmc filters`)
- Elevated (with the Administrators privilege)
- Go 1.22+ to build the CLI tool
- PowerShell 5.1+ with WinForms to run the GUI script.

## Building

On Windows,

```
.\scripts\build.ps1
```

On Linux,

```
make build
```

## Running silos

Launch another command in an existing named silo:

```powershell
bindmount silo exec my-silo -- cmd.exe
bindmount silo exec --detach my-silo -- pwsh.exe
```

Find the visible named silo containing a process by PID or executable name:

```powershell
bindmount silo find 1234
bindmount silo find node
```

The lookup reports every visible named Job Silo that contains the process,
including its name and Silo ID. Windows does not provide a way to resolve an
unnamed or inaccessible silo back to a Job Object name.

## License

MIT
