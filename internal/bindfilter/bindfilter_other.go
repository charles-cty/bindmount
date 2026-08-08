//go:build !windows

package bindfilter

import (
	"errors"

	"bindmount/internal/winapi"
)

// errNotWindows is returned by every entry point on non-Windows builds; the
// CLI reports it as a clear "Windows only" diagnostic.
var errNotWindows = errors.New("bindmount: Bind Filter is only available on Windows")

// Options controlling how a mapping is created. See the Windows build for
// field semantics.
type Options struct {
	ReadOnly   bool
	Merged     bool
	Exceptions []string
}

// Mapping is one decoded mapping entry.
type Mapping struct {
	VirtualRoot string
	Flags       uint32
	Targets     []string
}

// ErrNotWindows exposes the platform error for the CLI to detect.
func ErrNotWindows() error { return errNotWindows }

func CreateGlobal(virtualRoot, target string, opts Options) error { return errNotWindows }
func RemoveGlobal(virtualRoot string) error                       { return errNotWindows }
func CreateSilo(job winapi.Handle, virtualRoot, target string, opts Options) error {
	return errNotWindows
}
func RemoveSilo(job winapi.Handle, virtualRoot string) error { return errNotWindows }
func ListVolume(volumePath string) ([]Mapping, error)        { return nil, errNotWindows }
func ListSilo(job winapi.Handle) ([]Mapping, error)          { return nil, errNotWindows }
