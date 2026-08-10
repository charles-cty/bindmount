// Command decoy is a stand-in executable used by the GUI's "Block WSL"
// option. bindmount exec redirects every known wsl.exe location to this
// binary via file-level bind links, so any attempt to launch WSL inside the
// silo runs decoy instead, which explains the block and exits with an error.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "WSL is not available inside this environment.")
	os.Exit(1)
}
