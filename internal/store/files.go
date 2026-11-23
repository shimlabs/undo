package store

import (
	"bytes"
	"encoding/gob"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/shimlabs/undo/internal/atomicfile"
)

func readJSON(path string, value any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, value)
}

func writeJSON(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, data)
}

func readGob(path string, value any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return gob.NewDecoder(bytes.NewReader(data)).Decode(value)
}

func writeGob(path string, value any) error {
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(value); err != nil {
		return err
	}
	return writeFileAtomic(path, buf.Bytes())
}

func writeFileAtomic(path string, data []byte) error {
	return atomicfile.WriteFile(path, data, 0o600)
}

// makeWritable lets the store add and remove entries in dir. Directories in
// the base keep the project's modes, and a read-only one would otherwise stop
// merges. Their recorded modes live in the listings, not on disk.
func makeWritable(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o700 == 0o700 {
		return err
	}
	return os.Chmod(dir, info.Mode().Perm()|0o700)
}

// removeAll removes path and everything below it, including directories that
// were cloned read-only.
func removeAll(path string) error {
	err := os.RemoveAll(path)
	if !errors.Is(err, fs.ErrPermission) {
		return err
	}
	// Directories that cannot be made writable make the final RemoveAll fail,
	// and that error is the one to report.
	_ = filepath.WalkDir(path, func(dir string, entry fs.DirEntry, err error) error {
		if err == nil && entry.IsDir() {
			_ = makeWritable(dir)
		}
		return nil
	})
	if err := makeWritable(filepath.Dir(path)); err != nil {
		return err
	}
	return os.RemoveAll(path)
}
