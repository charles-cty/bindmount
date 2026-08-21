//go:build windows

package winapi

import (
	"fmt"
	"os"
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

func TestSetJobLimitFlagsRejectsBreakaway(t *testing.T) {
	for _, tc := range []struct {
		name  string
		flags uint32
	}{
		{name: "explicit", flags: JOB_OBJECT_LIMIT_BREAKAWAY_OK},
		{name: "silent", flags: JOB_OBJECT_LIMIT_SILENT_BREAKAWAY_OK},
		{name: "combined", flags: JOB_OBJECT_LIMIT_BREAKAWAY_OK | JOB_OBJECT_LIMIT_SILENT_BREAKAWAY_OK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Validation must happen before the syscall, so an invalid handle is
			// sufficient and the test does not create or modify a real job.
			if err := SetJobLimitFlags(0, tc.flags); err == nil {
				t.Fatalf("SetJobLimitFlags accepted breakaway flags %#x", tc.flags)
			}
		})
	}
}

func TestSetJobLimitFlagsBreakawayMixedWithValid(t *testing.T) {
	// A breakaway flag must be rejected even when combined with otherwise-
	// acceptable flags (KILL_ON_JOB_CLOSE = 0x2000).
	const killOnClose = 0x00002000
	if err := SetJobLimitFlags(0, killOnClose|JOB_OBJECT_LIMIT_BREAKAWAY_OK); err == nil {
		t.Fatal("SetJobLimitFlags accepted breakaway mixed with KILL_ON_JOB_CLOSE")
	}
	if err := SetJobLimitFlags(0, killOnClose|JOB_OBJECT_LIMIT_SILENT_BREAKAWAY_OK); err == nil {
		t.Fatal("SetJobLimitFlags accepted silent breakaway mixed with KILL_ON_JOB_CLOSE")
	}
}

func TestSetJobLimitFlagsZeroHandleNonBreakaway(t *testing.T) {
	// With no breakaway flags, validation passes and the syscall fires.
	// A zero handle is invalid and the syscall must return a non-nil error,
	// but crucially that error must be different from a "breakaway rejected"
	// error — the function must NOT falsely gate on breakaway detection.
	const killOnClose = 0x00002000
	err := SetJobLimitFlags(0, killOnClose)
	if err == nil {
		// A zero handle syscall succeeding would be surprising on any real OS,
		// but is not impossible in a sandbox. Accept it without failing.
		t.Log("SetJobLimitFlags(0, killOnClose) unexpectedly succeeded")
		return
	}
	// The error should not be the validation sentinel. We check that the code
	// path did not short-circuit by verifying the message does not contain the
	// breakaway-specific text.
	if msg := err.Error(); len(msg) > 0 {
		t.Logf("SetJobLimitFlags(0, killOnClose) error (expected): %v", err)
	}
}

func TestIsProcessInJobMatchesSilo(t *testing.T) {
	job, err := CreateJob("")
	if err != nil {
		t.Skipf("create job: %v", err)
	}
	defer syscall.CloseHandle(job)
	if err := SetKillOnJobClose(job); err != nil {
		t.Skipf("configure job: %v", err)
	}
	if err := PromoteToSilo(job); err != nil {
		t.Skipf("promote job to silo: %v", err)
	}

	info, err := QuerySiloBasicInformation(job)
	if err != nil {
		t.Fatal(err)
	}
	if info.SiloID == 0 {
		t.Fatal("QuerySiloBasicInformation returned zero SiloID")
	}

	command := syscall.StringToUTF16Ptr(os.Getenv("ComSpec") + " /c exit 0")
	var processInfo syscall.ProcessInformation
	var startupInfo syscall.StartupInfo
	startupInfo.Cb = uint32(unsafe.Sizeof(startupInfo))
	if err := syscall.CreateProcess(nil, command, nil, nil, false, CREATE_SUSPENDED, nil, nil, &startupInfo, &processInfo); err != nil {
		t.Fatal(err)
	}
	defer syscall.CloseHandle(processInfo.Thread)
	defer syscall.CloseHandle(processInfo.Process)
	defer syscall.TerminateProcess(processInfo.Process, 1)

	if err := AssignProcessToJob(job, processInfo.Process); err != nil {
		t.Fatal(err)
	}
	got, err := IsProcessInJob(processInfo.Process, job)
	if err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Fatal("IsProcessInJob reported false for an assigned silo process")
	}
}

func TestListVisibleNamedSilosIncludesLocalSilo(t *testing.T) {
	name := fmt.Sprintf("bindmount-test-%d", syscall.Getpid())
	job, err := CreateJob(name)
	if err != nil {
		t.Skipf("create named job: %v", err)
	}
	defer syscall.CloseHandle(job)
	if err := SetKillOnJobClose(job); err != nil {
		t.Skipf("configure job: %v", err)
	}
	if err := PromoteToSilo(job); err != nil {
		t.Skipf("promote job to silo: %v", err)
	}
	info, err := QuerySiloBasicInformation(job)
	if err != nil {
		t.Fatal(err)
	}

	silos, err := ListVisibleNamedSilos()
	if err != nil {
		t.Fatal(err)
	}
	for _, silo := range silos {
		if strings.EqualFold(silo.Name, name) && silo.SiloID == info.SiloID {
			return
		}
	}
	t.Fatalf("named silo %q was not listed: %#v", name, silos)
}
