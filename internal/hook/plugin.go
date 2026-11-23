package hook

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/shimlabs/undo/internal/atomicfile"
)

// pluginFile installs a plugin file that the agent loads on start. OpenCode
// uses this.
type pluginFile struct {
	content func(binary string) ([]byte, error)
}

var errForeignPlugin = errors.New("not written by undo, left unchanged")

// install writes the plugin, which starts binary without a shell.
func (p pluginFile) install(path, binary, _ string) error {
	if err := checkPluginOwner(path); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	content, err := p.content(binary)
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(path, content, 0o644)
}

func (p pluginFile) uninstall(path, _ string) error {
	if err := checkPluginOwner(path); err != nil {
		return err
	}
	err := os.Remove(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func (p pluginFile) isInstalled(path, _ string) (bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return isUndoPlugin(data), nil
}

// checkPluginOwner refuses to change a plugin file that undo did not write.
func checkPluginOwner(path string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !isUndoPlugin(data) {
		return &fs.PathError{Op: "write", Path: path, Err: errForeignPlugin}
	}
	return nil
}

func isUndoPlugin(data []byte) bool {
	return strings.HasPrefix(string(data), pluginHeader)
}
