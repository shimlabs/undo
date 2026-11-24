//go:build darwin || linux

// Command undo puts a project back the way it was before a coding agent ran a
// command.
package main

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"github.com/shimlabs/undo/internal/cli"
	"github.com/shimlabs/undo/internal/store"
	"github.com/shimlabs/undo/internal/terminal"
	"github.com/shimlabs/undo/internal/watch"
)

// workingDir returns the folder undo runs in. On Linux, getcwd(3) fails for
// a folder that was deleted, which is where a shell is after an agent
// removes the folder it is in, so the shell's $PWD names it instead.
func workingDir() (string, error) {
	dir, err := os.Getwd()
	if errors.Is(err, fs.ErrNotExist) {
		if shellDir := os.Getenv("PWD"); filepath.IsAbs(shellDir) {
			return filepath.Clean(shellDir), nil
		}
	}
	return dir, err
}

func main() {
	dir, err := workingDir()
	if err != nil {
		exitWithError(err)
	}
	executable, err := os.Executable()
	if err != nil {
		exitWithError(err)
	}
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	storeHome, err := store.DefaultHome()
	if err != nil {
		exitWithError(err)
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

func exitWithError(err error) {
	terminal.NewWriter(os.Stderr).Errorf("%s", terminal.Printable(err.Error()))
	os.Exit(1)
}

// startWatcher runs "undo watch" for root in its own session, so it keeps
// running after the command that started it exits. The returned channel
// receives the watcher's error if it exits while this process still runs,
// so a watcher that cannot start is noticed at once.
func startWatcher(executable, root string) (<-chan error, error) {
	cmd := exec.CommandContext(context.Background(), executable, "watch")
	cmd.Dir = root
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	return exited, nil
}
