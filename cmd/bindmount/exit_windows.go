//go:build windows

package main

import "os"

// exitWith maps a child exit code to the process exit code. Placed in a
// separate file so the os.Exit call is the single, auditable exit path.
func exitWith(code uint32) {
	os.Exit(int(code))
}
