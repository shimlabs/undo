// Command undo puts a project back the way it was before a coding agent ran a
// command.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"github.com/shimlabs/undo/internal/cli"
	"github.com/shimlabs/undo/internal/store"
	"github.com/shimlabs/undo/internal/watch"
)

func main() {
	dir, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "undo:", err)
		os.Exit(1)
	}
	executable, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "undo:", err)
		os.Exit(1)
	}
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	storeHome, err := store.DefaultHome()
	if err != nil {
		fmt.Fprintln(os.Stderr, "undo:", err)
		os.Exit(1)
	}
	os.Exit(cli.Run(os.Args[1:], cli.Env{
		Dir:        dir,
		Stdin:      os.Stdin,
		Stdout:     os.Stdout,
		Stderr:     os.Stderr,
		StoreHome:  storeHome,
		SocketDir:  watch.DefaultSocketDir(),
		Executable: executable,
		StartWatcher: func(root string) (<-chan error, error) {
			return startWatcher(executable, root)
		},
	}))
}

// startWatcher runs "undo watch" for root in its own session, so it keeps
// running after the command that started it exits. The returned channel
// receives the watcher's error if it exits while this process still runs,
// so a watcher that cannot start is noticed at once.
func startWatcher(executable, root string) (<-chan error, error) {
	cmd := exec.Command(executable, "watch")
	cmd.Dir = root
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	return exited, nil
}
