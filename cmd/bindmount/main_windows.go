//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"github.com/spf13/cobra"

	"bindmount/internal/bindfilter"
	"bindmount/internal/winapi"
)

func main() {
	if err := newRootCommand().Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "bindmount: %v\n", err)
		os.Exit(1)
	}
}

func newRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:   "bindmount",
		Short: "manage Windows Bind Filter mappings and Job Silos",
		Long: `Manage global or silo-scoped Windows Bind Filter mappings and launch
processes inside Job Silos with an optional isolated filesystem root.

Most mapping and silo operations require elevation. The Bf* interface used by
this tool is undocumented; see docs/BindFilterAPI.md for compatibility notes.`,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newAddCommand(), newRemoveCommand(), newListCommand(), newExecCommand())
	root.AddCommand(newSiloCommand())
	return root
}

func siloLookupNames(name string) []string {
	if len(name) >= len(`Global\`) && strings.EqualFold(name[:len(`Global\`)], `Global\`) {
		return []string{name}
	}
	if len(name) >= len(`Local\`) && strings.EqualFold(name[:len(`Local\`)], `Local\`) {
		return []string{name}
	}
	return []string{name, `Global\` + name}
}

// openSiloJob applies the same namespace resolution everywhere a named silo
// is opened: the caller's session namespace first, then Global\ for silos
// created in another session. Explicit Global\ and Local\ names are tried as-is.
func openSiloJob(name string, desiredAccess uint32) (winapi.Handle, error) {
	var lastErr error
	for _, candidate := range siloLookupNames(name) {
		job, err := winapi.OpenJob(candidate, desiredAccess)
		if err == nil {
			return job, nil
		}
		lastErr = err
		if !errors.Is(err, syscall.ERROR_FILE_NOT_FOUND) && !errors.Is(err, syscall.ERROR_PATH_NOT_FOUND) {
			return 0, err
		}
	}
	return 0, lastErr
}

func openNamedSilo(name string, desiredAccess uint32) (winapi.Handle, error) {
	var lastErr error
	foundNonSiloJob := false
	for _, candidate := range siloLookupNames(name) {
		job, err := winapi.OpenJob(candidate, desiredAccess)
		if err != nil {
			lastErr = err
			if !errors.Is(err, syscall.ERROR_FILE_NOT_FOUND) && !errors.Is(err, syscall.ERROR_PATH_NOT_FOUND) {
				return 0, err
			}
			continue
		}
		if _, err := winapi.QuerySiloBasicInformation(job); err == nil {
			return job, nil
		}
		syscall.CloseHandle(job)
		foundNonSiloJob = true
	}
	if foundNonSiloJob {
		return 0, fmt.Errorf("job %q is not a Job Silo", name)
	}
	return 0, lastErr
}

func newSiloCommand() *cobra.Command {
	silo := &cobra.Command{Use: "silo", Short: "inspect or terminate named Job Silos"}
	silo.AddCommand(newSiloExecCommand(), newSiloFindCommand())
	silo.AddCommand(&cobra.Command{
		Use: "exists <name>", Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			// This is an existence check, not an access check: the job must
			// count as existing whenever it is there, even if the caller's
			// token cannot open it. A plain name searches the caller's
			// session namespace; "Global\" searches session 0's namespace,
			// where silos created by an elevated process live. Try both.
			// ERROR_ACCESS_DENIED means the open reached the object but the
			// security descriptor rejected the access mask, which only
			// happens when the job exists.
			job, err := openSiloJob(args[0], winapi.JOB_OBJECT_QUERY)
			if err == nil {
				syscall.CloseHandle(job)
				fmt.Printf("bindmount: silo %q exists\n", args[0])
				return nil
			}
			if errors.Is(err, syscall.ERROR_ACCESS_DENIED) {
				fmt.Printf("bindmount: silo %q exists\n", args[0])
				return nil
			}
			if !errors.Is(err, syscall.ERROR_FILE_NOT_FOUND) && !errors.Is(err, syscall.ERROR_PATH_NOT_FOUND) {
				return fmt.Errorf("check silo %q: %w", args[0], err)
			}
			fmt.Printf("bindmount: silo %q does not exist\n", args[0])
			return nil
		},
	})
	silo.AddCommand(&cobra.Command{
		Use: "kill <name>", Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			// Mirror the exists check: try plain name first, then Global\ for
			// silos created by elevated processes in session 0.
			job, openErr := openSiloJob(args[0], winapi.JOB_OBJECT_TERMINATE)
			if openErr != nil {
				return fmt.Errorf("open silo %q: %w", args[0], openErr)
			}
			defer syscall.CloseHandle(job)
			if err := winapi.TerminateJob(job, 1); err != nil {
				return fmt.Errorf("kill silo %q: %w", args[0], err)
			}
			fmt.Printf("bindmount: terminated silo %q\n", args[0])
			return nil
		},
	})
	return silo
}

const siloExecUsage = "bindmount silo exec [--detach] <job-name> -- <command> [args...]"

type siloExecOptions struct {
	detach  bool
	jobName string
	cmdArgs []string
}

func newSiloExecCommand() *cobra.Command {
	return &cobra.Command{
		Use:                "exec [--detach] <job-name> -- <command> [args...]",
		Short:              "launch a command inside an existing Job Silo",
		Long:               "Open an existing named Job Silo and launch a command in its bind-link view. Use --detach to let the command keep the Silo alive after bindmount exits.",
		DisableFlagParsing: true,
		RunE: func(_ *cobra.Command, args []string) error {
			for _, arg := range args {
				if arg == "--" {
					break
				}
				if arg == "-h" || arg == "--help" {
					fmt.Println("usage: " + siloExecUsage)
					return nil
				}
			}
			return cmdSiloExec(args)
		},
	}
}

func cmdSiloExec(args []string) error {
	opts, err := parseSiloExecArgs(args)
	if err != nil {
		return err
	}
	job, err := openNamedSilo(opts.jobName, winapi.JOB_OBJECT_ALL_ACCESS)
	if err != nil {
		return fmt.Errorf("open silo %q: %w", opts.jobName, err)
	}
	defer syscall.CloseHandle(job)
	if opts.detach {
		if err := winapi.MakeHandleInheritable(job); err != nil {
			return fmt.Errorf("prepare detached silo handle: %w", err)
		}
	}

	exitCode, err := runInSilo(job, opts.cmdArgs, opts.detach, "")
	if shouldFallbackSiloLaunch(err) {
		exitCode, err = runInSiloFallback(job, opts.cmdArgs, opts.detach)
	}
	if err != nil {
		return fmt.Errorf("launch in silo %q: %w", opts.jobName, err)
	}
	if !opts.detach && exitCode != 0 {
		exitWith(exitCode)
	}
	return nil
}

func parseSiloExecArgs(args []string) (siloExecOptions, error) {
	separator := -1
	for index, arg := range args {
		if arg == "--" {
			separator = index
			break
		}
	}
	if separator < 0 {
		return siloExecOptions{}, errors.New(`silo exec requires "--" before the command`)
	}
	if separator == len(args)-1 {
		return siloExecOptions{}, errors.New("silo exec requires a command to run inside the silo")
	}

	var opts siloExecOptions
	var positionals []string
	for _, arg := range args[:separator] {
		switch arg {
		case "--detach":
			opts.detach = true
		default:
			if strings.HasPrefix(arg, "-") {
				return siloExecOptions{}, fmt.Errorf("unknown silo exec flag %q", arg)
			}
			positionals = append(positionals, arg)
		}
	}
	if len(positionals) != 1 {
		return siloExecOptions{}, errors.New("silo exec requires exactly one job name")
	}
	opts.jobName = positionals[0]
	opts.cmdArgs = args[separator+1:]
	return opts, nil
}

func newSiloFindCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "find <pid-or-process-name>",
		Short: "find visible named Job Silos containing a process",
		Long:  "Find visible named Job Silos containing a process identified by a PID or executable name. A name matches case-insensitively and may omit .exe. Windows cannot resolve unnamed or inaccessible silos to a Job Object name.",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return cmdSiloFind(args[0])
		},
	}
}

type processReference struct {
	pid  uint32
	name string
}

func cmdSiloFind(identifier string) error {
	processes, err := matchingProcesses(identifier)
	if err != nil {
		return err
	}
	silos, err := winapi.ListVisibleNamedSilos()
	if err != nil {
		return err
	}
	for _, process := range processes {
		handle, err := syscall.OpenProcess(winapi.PROCESS_QUERY_INFORMATION, false, process.pid)
		if err != nil {
			return fmt.Errorf("open process %d (%s): %w", process.pid, process.name, err)
		}
		matches, matchErr := namedSilosForProcess(handle, silos)
		syscall.CloseHandle(handle)
		if matchErr != nil {
			return fmt.Errorf("query process %d (%s): %w", process.pid, process.name, matchErr)
		}
		if len(matches) == 0 {
			fmt.Printf("bindmount: PID %d (%s): no visible named Job Silo\n", process.pid, process.name)
			continue
		}
		for _, silo := range matches {
			fmt.Printf("bindmount: PID %d (%s): silo %q (ID %d)\n", process.pid, process.name, silo.Name, silo.SiloID)
		}
	}
	return nil
}

func namedSilosForProcess(process winapi.Handle, silos []winapi.NamedSilo) ([]winapi.NamedSilo, error) {
	matches := make([]winapi.NamedSilo, 0, 1)
	for _, silo := range silos {
		job, err := winapi.OpenJob(silo.Name, winapi.JOB_OBJECT_QUERY)
		if err != nil {
			if errors.Is(err, syscall.ERROR_ACCESS_DENIED) ||
				errors.Is(err, syscall.ERROR_FILE_NOT_FOUND) ||
				errors.Is(err, syscall.ERROR_PATH_NOT_FOUND) {
				continue
			}
			return nil, fmt.Errorf("open silo %q: %w", silo.Name, err)
		}
		inJob, queryErr := winapi.IsProcessInJob(process, job)
		syscall.CloseHandle(job)
		if queryErr != nil {
			return nil, queryErr
		}
		if inJob {
			matches = append(matches, silo)
		}
	}
	return matches, nil
}

func matchingProcesses(identifier string) ([]processReference, error) {
	if pid, err := strconv.ParseUint(identifier, 10, 32); err == nil {
		if pid == 0 {
			return nil, errors.New("PID must be greater than zero")
		}
		processes, err := allProcesses()
		if err != nil {
			return nil, err
		}
		for _, process := range processes {
			if process.pid == uint32(pid) {
				return []processReference{process}, nil
			}
		}
		return nil, fmt.Errorf("process with PID %d does not exist", pid)
	}

	processes, err := allProcesses()
	if err != nil {
		return nil, err
	}
	var matches []processReference
	for _, process := range processes {
		if matchesProcessName(process.name, identifier) {
			matches = append(matches, process)
		}
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("no running process named %q", identifier)
	}
	return matches, nil
}

func allProcesses() ([]processReference, error) {
	snapshot, err := syscall.CreateToolhelp32Snapshot(syscall.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, fmt.Errorf("create process snapshot: %w", err)
	}
	defer syscall.CloseHandle(snapshot)

	var entry syscall.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	if err := syscall.Process32First(snapshot, &entry); err != nil {
		return nil, fmt.Errorf("read first process: %w", err)
	}
	var processes []processReference
	for {
		processes = append(processes, processReference{
			pid:  entry.ProcessID,
			name: syscall.UTF16ToString(entry.ExeFile[:]),
		})
		if err := syscall.Process32Next(snapshot, &entry); err != nil {
			if errors.Is(err, syscall.ERROR_NO_MORE_FILES) {
				break
			}
			return nil, fmt.Errorf("read next process: %w", err)
		}
	}
	sort.Slice(processes, func(i, j int) bool { return processes[i].pid < processes[j].pid })
	return processes, nil
}

func matchesProcessName(name, query string) bool {
	if strings.EqualFold(name, query) {
		return true
	}
	return !strings.Contains(query, ".") && strings.EqualFold(name, query+".exe")
}

func newExecCommand() *cobra.Command {
	// exec has a command payload after `--`; leave that payload untouched so
	// Cobra does not try to interpret the child command's own flags.
	return &cobra.Command{Use: "exec [flags] <job-name> -- <command> [args...]", Short: "create a Job Silo and launch a command", Long: "Create a named Job Silo, optionally install root and scoped mappings, and launch a command inside it. With --root, executable, PATH, current-directory, Git-root, and app-execution-alias passthrough are enabled by default; appstate and powershell passthrough are always opt-in. The powershell passthrough maps TEMP and TMP to a per-silo directory under %LOCALAPPDATA%\\bindmount\\tempdirs and clears it before launch. Disable individual types with --no-passthrough <name>. --readonly-root is mutually exclusive with --root and does not change passthrough defaults. Use --no-ui-restrictions for applications such as Electron that create nested Job Objects.", DisableFlagParsing: true, RunE: func(_ *cobra.Command, args []string) error {
		// Only treat -h/--help as a help request when it appears before the
		// "--" separator; after it the payload belongs to the child command.
		for _, arg := range args {
			if arg == "--" {
				break
			}
			if arg == "-h" || arg == "--help" {
				fmt.Println("usage: " + execUsage)
				return nil
			}
		}
		return cmdExec(args)
	}}
}

// scopeFlags holds the --silo handling shared by add/remove/list.
type siloScope struct {
	name string // empty = global
}

// open resolves the scope to a job handle (0 for global). The returned close
// function must be called when the handle is no longer needed.
func (s *siloScope) open(desiredAccess uint32) (job winapi.Handle, closeFn func(), err error) {
	if s.name == "" {
		return 0, func() {}, nil
	}
	h, err := openSiloJob(s.name, desiredAccess)
	if err != nil {
		return 0, nil, fmt.Errorf("open silo job %q: %w", s.name, err)
	}
	return h, func() { syscall.CloseHandle(syscall.Handle(h)) }, nil
}

func newAddCommand() *cobra.Command {
	var silo string
	cmd := &cobra.Command{Use: "add <virtual-root> <target> | <root[+][=|==]target>", Args: func(_ *cobra.Command, args []string) error {
		if len(args) != 1 && len(args) != 2 {
			return errors.New("add requires either one root=target spec or two path arguments")
		}
		return nil
	}, RunE: func(_ *cobra.Command, args []string) error {
		readOnly, merged := false, false
		if len(args) == 1 {
			root, target, specReadOnly, specMerged, ok := splitLinkSpec(args[0])
			if !ok {
				return fmt.Errorf("invalid mapping %q: want root[+][=|==]target", args[0])
			}
			args = []string{root, target}
			readOnly = specReadOnly
			merged = specMerged
		}
		return addMapping(args[0], args[1], readOnly, merged, silo)
	}}
	cmd.Flags().StringVar(&silo, "silo", "", "scope the mapping to a silo job")
	return cmd
}

func addMapping(virtualRoot, target string, readOnly, merged bool, silo string) error {
	scope := siloScope{name: silo}
	job, closeJob, err := scope.open(winapi.JOB_OBJECT_ALL_ACCESS)
	if err != nil {
		return err
	}
	defer closeJob()

	opts := bindfilter.Options{ReadOnly: readOnly, Merged: merged}
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
	fmt.Printf("bindmount: created %s mapping %s -> %s\n", kind, virtualRoot, target)
	return nil
}

func newRemoveCommand() *cobra.Command {
	var silo string
	cmd := &cobra.Command{Use: "remove <virtual-root>", Args: cobra.ExactArgs(1), RunE: func(_ *cobra.Command, args []string) error { return removeMapping(args[0], silo) }}
	cmd.Flags().StringVar(&silo, "silo", "", "scope the removal to a silo job")
	return cmd
}

func removeMapping(virtualRoot, silo string) error {
	scope := siloScope{name: silo}
	job, closeJob, err := scope.open(winapi.JOB_OBJECT_ALL_ACCESS)
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
	fmt.Printf("bindmount: removed %s mapping %s\n", kind, virtualRoot)
	return nil
}

func newListCommand() *cobra.Command {
	var silo string
	cmd := &cobra.Command{Use: "list [volume-path]", Args: cobra.MaximumNArgs(1), RunE: func(_ *cobra.Command, args []string) error {
		volume := `C:\`
		if len(args) == 1 {
			volume = args[0]
		}
		return listMappings(volume, silo)
	}}
	cmd.Flags().StringVar(&silo, "silo", "", "list mappings in a silo job")
	return cmd
}

func listMappings(volume, silo string) error {
	scope := siloScope{name: silo}
	var mappings []bindfilter.Mapping
	var err error
	if scope.name == "" {
		mappings, err = bindfilter.ListVolume(volume)
	} else {
		var job winapi.Handle
		var closeJob func()
		job, closeJob, err = scope.open(winapi.JOB_OBJECT_ALL_ACCESS)
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
		fmt.Println("bindmount: no mappings")
		return nil
	}
	for _, m := range mappings {
		fmt.Printf("bindmount: %s\n  flags: 0x%08X\n", m.VirtualRoot, m.Flags)
		for _, t := range m.Targets {
			fmt.Printf("  -> %s\n", t)
		}
	}
	return nil
}
