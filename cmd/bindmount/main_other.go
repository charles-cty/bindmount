//go:build !windows

package main

import (
	"fmt"
	"os"
	"runtime"
)

func main() {
	fmt.Fprintf(os.Stderr, "bindmount: this tool manages the Windows Bind Filter and only runs on Windows (GOOS=%s)\n", runtime.GOOS)
	os.Exit(2)
}
