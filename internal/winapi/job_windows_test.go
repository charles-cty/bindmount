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
