//go:build !darwin && !linux

// Command undo puts a project back the way it was before a coding agent ran a
// command.
package main

import (
	"fmt"
	"os"
)

// main explains that this system is not supported. undo depends on
// copy-on-write clones and file events that it uses only on macOS and Linux.
func main() {
	fmt.Fprintln(os.Stderr, "undo runs on macOS and Linux only.")
	os.Exit(1)
}
