// Command undo puts a project back the way it was before a coding agent ran a
// command.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/shimlabs/undo/internal/cli"
)

func main() {
	dir, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "undo:", err)
		os.Exit(1)
	}
	os.Exit(cli.Run(os.Args[1:], cli.Env{
		Dir:          dir,
		Stdin:        os.Stdin,
		Stdout:       os.Stdout,
		Stderr:       os.Stderr,
		StartWatcher: startWatcher,
	}))
}

// startWatcher runs "undo watch" for root in its own session, so it keeps
// running after the command that started it exits.
func startWatcher(root string) error {
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(binary, "watch")
	cmd.Dir = root
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
