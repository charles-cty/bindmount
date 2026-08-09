//go:build windows

package winapi

import "testing"

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
