//go:build windows

package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"syscall"

	"bindmount/internal/bindfilter"
	"bindmount/internal/winapi"
)

const usageText = `bindmount - manage Windows Bind Links via the Bind Filter (bindflt.sys)

Usage:
  bindmount <command> [arguments]

Commands:
  add <virtual-root> <target>     Create a bind link
        [--read-only]               Make the mapping read-only
        [--merged]                  Merge virtual root contents with the target
        [--silo <job-name>]         Scope the mapping to a silo job (default: global)

  remove <virtual-root>           Remove a bind link
        [--silo <job-name>]         Scope: remove from a silo job (default: global)

  list [<volume-path>]            List mappings on a volume (default: C:\)
        [--silo <job-name>]         List mappings of a silo job instead

  exec <job-name> <command>...    Create a silo job, run the command inside it,
                                    and terminate the job when it exits.
                                    Use with add/remove --silo to set up links first.

Notes:
  - add/remove/list require elevation (SeDebugPrivilege-equivalent access is
    enforced by the driver, not by this tool).
  - Silo job names are looked up with OpenJobObject; the job must already be a
    silo for add, and must exist for remove/list.
  - This tool uses the undocumented Bf* interface of bindfltapi.dll. See
    docs/BindFilterAPI.md. It is not a stable contract.
`

func usage(exitCode int) {
	fmt.Fprint(os.Stderr, usageText)
	os.Exit(exitCode)
}

func main() {
	if len(os.Args) < 2 {
		usage(2)
	}

	var err error
	switch os.Args[1] {
	case "add":
		err = cmdAdd(os.Args[2:])
	case "remove":
		err = cmdRemove(os.Args[2:])
	case "list":
		err = cmdList(os.Args[2:])
	case "exec":
		err = cmdExec(os.Args[2:])
	case "-h", "--help", "help":
		usage(0)
	default:
		fmt.Fprintf(os.Stderr, "bindmount: unknown command %q\n\n", os.Args[1])
		usage(2)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "bindmount: %v\n", err)
		os.Exit(1)
	}
}

// scopeFlags holds the --silo handling shared by add/remove/list.
type siloScope struct {
	name string // empty = global
}

func (s *siloScope) register(fs *flag.FlagSet) {
	fs.StringVar(&s.name, "silo", "", "scope the operation to the named silo job")
}

// open resolves the scope to a job handle (0 for global). The returned close
// function must be called when the handle is no longer needed.
func (s *siloScope) open() (job winapi.Handle, closeFn func(), err error) {
	if s.name == "" {
		return 0, func() {}, nil
	}
	h, err := winapi.OpenJob(s.name, winapi.JOB_OBJECT_ALL_ACCESS)
	if err != nil {
		return 0, nil, fmt.Errorf("open silo job %q: %w", s.name, err)
	}
	return h, func() { syscall.CloseHandle(syscall.Handle(h)) }, nil
}

func cmdAdd(args []string) error {
	fs := flag.NewFlagSet("add", flag.ExitOnError)
	readOnly := fs.Bool("read-only", false, "create a read-only mapping")
	merged := fs.Bool("merged", false, "merge the virtual root with the target")
	var scope siloScope
	scope.register(fs)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: bindmount add [--read-only] [--merged] [--silo <job-name>] <virtual-root> <target>")
	}
	fs.Parse(args)

	if fs.NArg() != 2 {
		fs.Usage()
		return errors.New("add requires exactly two path arguments")
	}
	virtualRoot, target := fs.Arg(0), fs.Arg(1)

	job, closeJob, err := scope.open()
	if err != nil {
		return err
	}
	defer closeJob()

	opts := bindfilter.Options{ReadOnly: *readOnly, Merged: *merged}
	if scope.name == "" {
		err = bindfilter.CreateGlobal(virtualRoot, target, opts)
	} else {
		err = bindfilter.CreateSilo(job, virtualRoot, target, opts)
	}
	if err != nil {
		return fmt.Errorf("create mapping %s -> %s: %w", virtualRoot, target, err)
	}

	kind := "global"
	if scope.name != "" {
		kind = "silo " + scope.name
	}
	fmt.Printf("created %s mapping %s -> %s\n", kind, virtualRoot, target)
	return nil
}

func cmdRemove(args []string) error {
	fs := flag.NewFlagSet("remove", flag.ExitOnError)
	var scope siloScope
	scope.register(fs)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: bindmount remove [--silo <job-name>] <virtual-root>")
	}
	fs.Parse(args)

	if fs.NArg() != 1 {
		fs.Usage()
		return errors.New("remove requires exactly one path argument")
	}
	virtualRoot := fs.Arg(0)

	job, closeJob, err := scope.open()
	if err != nil {
		return err
	}
	defer closeJob()

	if scope.name == "" {
		err = bindfilter.RemoveGlobal(virtualRoot)
	} else {
		err = bindfilter.RemoveSilo(job, virtualRoot)
	}
	if err != nil {
		return fmt.Errorf("remove mapping %s: %w", virtualRoot, err)
	}

	kind := "global"
	if scope.name != "" {
		kind = "silo " + scope.name
	}
	fmt.Printf("removed %s mapping %s\n", kind, virtualRoot)
	return nil
}

func cmdList(args []string) error {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	var scope siloScope
	scope.register(fs)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: bindmount list [--silo <job-name>] [<volume-path>]")
	}
	fs.Parse(args)

	var mappings []bindfilter.Mapping
	var err error
	if scope.name == "" {
		volume := `C:\`
		if fs.NArg() > 0 {
			volume = fs.Arg(0)
		}
		mappings, err = bindfilter.ListVolume(volume)
	} else {
		var job winapi.Handle
		var closeJob func()
		job, closeJob, err = scope.open()
		if err != nil {
			return err
		}
		defer closeJob()
		mappings, err = bindfilter.ListSilo(job)
	}
	if err != nil {
		return fmt.Errorf("list mappings: %w", err)
	}

	if len(mappings) == 0 {
		fmt.Println("no mappings")
		return nil
	}
	for _, m := range mappings {
		fmt.Printf("%s\n  flags: 0x%08X\n", m.VirtualRoot, m.Flags)
		for _, t := range m.Targets {
			fmt.Printf("  -> %s\n", t)
		}
	}
	return nil
}
