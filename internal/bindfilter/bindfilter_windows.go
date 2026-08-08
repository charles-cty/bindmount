//go:build windows

package bindfilter

import (
	"errors"
	"fmt"
	"strings"
	"syscall"

	"bindmount/internal/winapi"
)

// Options controlling how a mapping is created.
type Options struct {
	ReadOnly   bool     // BINDFLT_FLAG_READ_ONLY_MAPPING
	Merged     bool     // BINDFLT_FLAG_MERGED_BIND_MAPPING
	Exceptions []string // excluded paths (rarely used; hcsshim passes none)
}

// setupFlags converts Options plus scope flags into the BfSetupFilter flag
// word. For global mappings we additionally set NO_MULTIPLE_TARGETS, matching
// the go-winio helper's defensive choice.
func (o Options) setupFlags(scopeFlags uint32) uint32 {
	flags := scopeFlags
	if o.ReadOnly {
		flags |= winapi.BINDFLT_FLAG_READ_ONLY_MAPPING
	}
	if o.Merged {
		flags |= winapi.BINDFLT_FLAG_MERGED_BIND_MAPPING
	}
	return flags
}

// CreateGlobal creates a mapping visible to all processes on the host.
// The mapping must later be removed with RemoveGlobal.
func CreateGlobal(virtualRoot, target string, opts Options) error {
	if virtualRoot == "" || target == "" {
		return errors.New("virtual root and target are required")
	}
	flags := opts.setupFlags(winapi.BINDFLT_FLAG_NO_MULTIPLE_TARGETS)
	return winapi.SetupFilter(0, flags, virtualRoot, target, opts.Exceptions)
}

// RemoveGlobal removes a global mapping by virtual root.
func RemoveGlobal(virtualRoot string) error {
	if virtualRoot == "" {
		return errors.New("virtual root is required")
	}
	return winapi.RemoveMapping(0, virtualRoot)
}

// CreateSilo creates a mapping scoped to the given silo job handle. The job
// must already have been promoted to a silo.
func CreateSilo(job winapi.Handle, virtualRoot, target string, opts Options) error {
	if job == 0 {
		return errors.New("a silo mapping requires a job handle")
	}
	if virtualRoot == "" || target == "" {
		return errors.New("virtual root and target are required")
	}
	flags := opts.setupFlags(winapi.BINDFLT_FLAG_USE_CURRENT_SILO_MAPPING)
	return winapi.SetupFilter(job, flags, virtualRoot, target, opts.Exceptions)
}

// RemoveSilo removes the mapping for virtualRoot in the scope of job.
func RemoveSilo(job winapi.Handle, virtualRoot string) error {
	if job == 0 {
		return errors.New("a silo mapping requires a job handle")
	}
	return winapi.RemoveMapping(job, virtualRoot)
}

// Mapping is one decoded mapping entry with DOS-style paths.
type Mapping struct {
	VirtualRoot string
	Flags       uint32
	Targets     []string
}

// ListVolume returns the mappings whose virtual roots live on the volume
// containing volumePath (any existing path on the volume works). NT device
// target paths returned by the filter are converted to DOS paths; a target
// that fails conversion is kept in raw form so the output stays truthful.
func ListVolume(volumePath string) ([]Mapping, error) {
	if volumePath == "" {
		return nil, errors.New("a volume path is required")
	}
	raw, err := getMappings(winapi.BINDFLT_GET_MAPPINGS_FLAG_VOLUME, 0, volumePath)
	if err != nil {
		return nil, err
	}
	return convertTargets(raw), nil
}

// ListSilo returns the mappings scoped to the given silo job handle.
func ListSilo(job winapi.Handle) ([]Mapping, error) {
	if job == 0 {
		return nil, errors.New("a silo query requires a job handle")
	}
	raw, err := getMappings(winapi.BINDFLT_GET_MAPPINGS_FLAG_SILO, job, "")
	if err != nil {
		return nil, err
	}
	return convertTargets(raw), nil
}

// getMappings performs the BfGetMappings call with a grow-and-retry strategy.
// go-winio uses a fixed 256-KB buffer; we start smaller and grow when the
// filter reports insufficient room, as recommended in docs/BindFilterAPI.md.
func getMappings(flags uint32, job winapi.Handle, rootPath string) ([]winapi.Mapping, error) {
	size := 64 * 1024
	for attempt := 0; attempt < 4; attempt++ {
		buf := make([]byte, size)
		n, err := winapi.GetMappingsRaw(flags, job, rootPath, nil, buf)
		if err == nil {
			return winapi.ParseMappingsChecked(buf, n)
		}
		if !errors.Is(err, syscall.ERROR_INSUFFICIENT_BUFFER) && !errors.Is(err, syscall.ERROR_MORE_DATA) {
			return nil, err
		}
		if n <= uint32(size) {
			n = uint32(size * 2)
		}
		size = int(n)
	}
	return nil, fmt.Errorf("BfGetMappings still reports insufficient buffer after growing to %d bytes", size)
}

// convertTargets converts NT device target paths to DOS paths where possible.
func convertTargets(in []winapi.Mapping) []Mapping {
	out := make([]Mapping, 0, len(in))
	for _, m := range in {
		targets := make([]string, 0, len(m.Targets))
		for _, t := range m.Targets {
			if strings.HasPrefix(t, `\Device\`) {
				if dos, err := winapi.NTPathToDOS(t); err == nil {
					t = dos
				}
			}
			targets = append(targets, t)
		}
		out = append(out, Mapping{VirtualRoot: m.VirtualRoot, Flags: m.Flags, Targets: targets})
	}
	return out
}
