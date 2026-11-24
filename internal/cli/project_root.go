package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/shimlabs/undo/internal/clone"
	"github.com/shimlabs/undo/internal/project"
	"github.com/shimlabs/undo/internal/store"
)

// projectRoot returns the root of the project in env.Dir, or of the project
// that --project names. Either folder may have been deleted, as when the
// shell is still in a folder an agent removed.
func projectRoot(env Env, parsed flags) (string, error) {
	dir := env.Dir
	if parsed.project != "" {
		dir = absolutePath(parsed.project, env.Dir)
	}
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		return findDeletedRoot(env.StoreHome, dir)
	} else if err != nil {
		return "", err
	}
	return project.Root(dir)
}

// existingProjectRoot returns the root of the project in env.Dir, or of the
// project that --project names, for a command that needs its folder.
func existingProjectRoot(env Env, parsed flags) (string, error) {
	dir := env.Dir
	if parsed.project != "" {
		dir = absolutePath(parsed.project, env.Dir)
	}
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		err := fmt.Errorf("%s %w", displayPath(dir), errFolderMissing)
		if root, rootErr := findDeletedRoot(env.StoreHome, dir); rootErr == nil && hasStore(env.StoreHome, root) {
			return "", withHint(err, fmt.Sprintf("Run %s to bring it back.", env.stderr().Bold(projectCommand(env, parsed, "undo"))))
		}
		return "", err
	}
	return project.Root(dir)
}

// openStore opens the existing store of the project. It returns an error
// matching fs.ErrNotExist if the project has no checkpoints yet.
func openStore(env Env, parsed flags) (*store.Store, error) {
	root, err := projectRoot(env, parsed)
	if err != nil {
		return nil, err
	}
	return store.Open(env.StoreHome, root)
}

// createStore opens the store of the project, creating it if needed. The
// project's folder must exist. Without --copy, a new store is made only
// where the filesystem can clone; with it, only where it cannot.
func createStore(env Env, parsed flags) (*store.Store, error) {
	root, err := existingProjectRoot(env, parsed)
	if err != nil {
		return nil, err
	}
	if parsed.isCopyMode {
		// Copy mode stays on for good, so where clones work it only costs
		// disk.
		if clone.IsSupportedIn(root) {
			return nil, usageError{message: "this filesystem can clone, so --copy is not needed here"}
		}
		return store.Create(env.StoreHome, root)
	}
	return store.CreateIfCloneable(env.StoreHome, root)
}

// findDeletedRoot returns the root of the project that held dir, which is
// gone. That is the nearest gone folder at or above dir with a store, or,
// when only a folder inside a project was deleted, the project of the
// nearest folder that still exists. Without either, dir is taken as the
// root.
func findDeletedRoot(home, dir string) (string, error) {
	dir, existing, err := resolveExistingPrefix(dir)
	if err != nil {
		return "", err
	}
	for candidate := dir; candidate != existing; candidate = filepath.Dir(candidate) {
		if hasStore(home, candidate) {
			return candidate, nil
		}
	}
	if root, err := project.Root(existing); err == nil && hasStore(home, root) {
		return root, nil
	}
	return dir, nil
}

// resolveExistingPrefix returns path with the part of it that still exists
// resolved, as stores record roots, and that part resolved.
func resolveExistingPrefix(path string) (resolved, resolvedPrefix string, err error) {
	prefix := filepath.Dir(path)
	for !exists(prefix) && prefix != filepath.Dir(prefix) {
		prefix = filepath.Dir(prefix)
	}
	if resolvedPrefix, err = filepath.EvalSymlinks(prefix); err != nil {
		return "", "", err
	}
	rest, err := filepath.Rel(prefix, path)
	if err != nil {
		return "", "", err
	}
	return filepath.Join(resolvedPrefix, rest), resolvedPrefix, nil
}

// hasStore reports whether the project at root has a store.
func hasStore(home, root string) bool {
	projectStore, err := store.Open(home, root)
	if err != nil {
		return false
	}
	projectStore.Close()
	return true
}

func absolutePath(path, dir string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(dir, path)
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}
