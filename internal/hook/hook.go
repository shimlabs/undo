// Package hook connects undo to coding agents: it installs a hook into each
// agent's configuration and takes a checkpoint when the agent calls it.
package hook

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/shimlabs/undo/internal/project"
	"github.com/shimlabs/undo/internal/store"
)

// Agent is a coding agent that undo can install a hook into.
type Agent struct {
	// Name is how the agent is named on the command line.
	Name string
	// Title is the agent's product name.
	Title string
	// Note tells the user what happens after the hook is installed.
	Note string

	configPath func() (string, error)
	installer  installer
	// parseEvent reads one hook event in the agent's format.
	parseEvent func(in io.Reader) (hookEvent, error)
	// isHandledElsewhere reports whether another agent's hook already takes
	// a checkpoint for event.
	isHandledElsewhere func(event hookEvent) bool
}

// hookTimeout is how long an agent waits for undo's hook before it gives up
// and runs the tool anyway. A first checkpoint of a large project takes a few
// seconds.
const hookTimeout = 30 * time.Second

// Agents returns every agent undo supports.
func Agents() []Agent {
	return []Agent{claude, codex, cursor, gemini, openCode}
}

// FindAgent returns the agent with the given command-line name.
func FindAgent(name string) (Agent, bool) {
	agents := Agents()
	i := slices.IndexFunc(agents, func(a Agent) bool { return a.Name == name })
	if i < 0 {
		return Agent{}, false
	}
	return agents[i], true
}

// ErrUnsafeBinary means the undo binary lives where it will disappear or
// where other users can replace it, so a hook must not run it.
var ErrUnsafeBinary = errors.New("this undo binary is in a temporary or shared directory; install undo with go install or a package manager first")

// Install adds a hook that runs binary before the agent changes files. A hook
// undo installed before is replaced, and everything else in the agent's
// configuration is kept. It returns the path of the file it wrote.
func (a Agent) Install(binary string) (string, error) {
	if err := checkBinary(binary); err != nil {
		return "", err
	}
	path, err := a.configPath()
	if err != nil {
		return "", err
	}
	return path, a.installer.install(path, binary, a.Name)
}

// Uninstall removes the hook added by Install and leaves everything else as
// it was. It returns the path of the file it changed.
func (a Agent) Uninstall() (string, error) {
	path, err := a.configPath()
	if err != nil {
		return "", err
	}
	return path, a.installer.uninstall(path, a.Name)
}

// IsInstalled reports whether undo's hook is in the agent's configuration.
func (a Agent) IsInstalled() bool {
	path, err := a.configPath()
	return err == nil && a.installer.isInstalled(path, a.Name)
}

// checkBinary refuses a binary in the temporary directory, where go run
// builds and which is cleaned up later, or in a directory that other users
// can write to, where anyone could put a program in its place.
func checkBinary(binary string) error {
	temps := []string{os.TempDir()}
	if resolved, err := filepath.EvalSymlinks(os.TempDir()); err == nil {
		temps = append(temps, resolved)
	}
	for _, temp := range temps {
		if strings.HasPrefix(binary, filepath.Clean(temp)+string(filepath.Separator)) {
			return fmt.Errorf("%s: %w", binary, ErrUnsafeBinary)
		}
	}
	info, err := os.Stat(filepath.Dir(binary))
	if err != nil {
		return nil
	}
	if info.Mode().Perm()&0o002 != 0 {
		return fmt.Errorf("%s: %w", binary, ErrUnsafeBinary)
	}
	return nil
}

// Handle reads one hook event from in and takes a checkpoint of the project
// the agent session works in, with the stores under storeHome. Errors are
// logged in the project's store, or under storeHome when there is none yet,
// and the agent is never blocked.
func (a Agent) Handle(in io.Reader, storeHome string, checkpoint store.Checkpointer) error {
	projectStore, err := a.handle(in, storeHome, checkpoint)
	switch {
	case err == nil:
	case projectStore != nil:
		projectStore.LogError(err)
	default:
		store.LogHomeError(storeHome, fmt.Errorf("%s hook: %w", a.Name, err))
	}
	return err
}

// handle takes the checkpoint and returns the project's store, if it got as
// far as opening one.
func (a Agent) handle(in io.Reader, storeHome string, checkpoint store.Checkpointer) (*store.Store, error) {
	event, err := a.parseEvent(in)
	if err != nil {
		return nil, err
	}
	if a.isHandledElsewhere != nil && a.isHandledElsewhere(event) {
		return nil, nil
	}
	projectDir := cmp.Or(event.ProjectDir, event.Dir)
	if projectDir == "" {
		if projectDir, err = os.Getwd(); err != nil {
			return nil, err
		}
	}
	root, err := project.Root(projectDir)
	if err != nil {
		return nil, err
	}
	projectStore, err := store.Create(storeHome, root)
	if err != nil {
		return nil, err
	}
	options := store.CheckpointOptions{Label: event.label(root), Dir: cmp.Or(event.Dir, projectDir), Agent: a.Name, Session: event.Session}
	_, err = checkpoint(projectStore, options)
	return projectStore, err
}

func homePath(parts ...string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(append([]string{home}, parts...)...), nil
}
