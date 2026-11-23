// Package exclude decides which paths undo leaves out of checkpoints.
package exclude

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"

	"golang.org/x/sys/unix"
)

// defaultPatterns lists directories undo skips unless .undoignore brings one
// back with a "!" line: directories that tools rebuild on their own, and the
// stores undo keeps next to projects on other volumes.
func defaultPatterns() []string {
	return []string{
		"node_modules/", ".venv/", "venv/", "__pycache__/", "target/", ".next/", ".turbo/", ".gradle/", ".cache/",
		".undo-*/",
	}
}

// Matcher checks project paths against the exclude patterns.
type Matcher struct {
	patterns []string
}

// IgnoreFile is the file in a project root that holds its own exclude
// rules, one gitignore-style pattern per line.
const IgnoreFile = ".undoignore"

// maxIgnoreFileSize and maxPatterns bound what .undoignore can hold. Every
// pattern is checked against every path in the project, and a real file
// holds a few dozen lines.
const (
	maxIgnoreFileSize = 64 << 10
	maxPatterns       = 1000
)

var (
	// ErrIgnoreFileNotRegular means .undoignore is a device, a named pipe
	// or a directory, which reading could block or never end.
	ErrIgnoreFileNotRegular = errors.New(IgnoreFile + " must be a regular file")
	// ErrIgnoreFileOutsideProject means .undoignore is a symlink that leads
	// out of the project.
	ErrIgnoreFileOutsideProject = errors.New(IgnoreFile + " must be a file inside the project")
	// ErrIgnoreFileTooLarge means .undoignore holds more than undo reads.
	ErrIgnoreFileTooLarge = fmt.Errorf("%s must be under %d KB and %d patterns", IgnoreFile, maxIgnoreFileSize>>10, maxPatterns)
)

// New returns a Matcher with the defaults plus the patterns in the project's
// .undoignore file.
func New(root string) (*Matcher, error) {
	patterns := defaultPatterns()
	file, err := openIgnoreFile(root)
	if errors.Is(err, fs.ErrNotExist) {
		return &Matcher{patterns: patterns}, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(io.LimitReader(file, maxIgnoreFileSize+1))
	// One line may be as long as the whole file may be.
	scanner.Buffer(make([]byte, 0, 4096), maxIgnoreFileSize+2)
	size, count := 0, 0
	for scanner.Scan() {
		size += len(scanner.Bytes()) + 1
		line := strings.TrimSpace(scanner.Text())
		switch {
		case line == "" || strings.HasPrefix(line, "#"):
		case strings.HasPrefix(line, "!"):
			name := strings.TrimSuffix(line[1:], "/")
			patterns = slices.DeleteFunc(patterns, func(pattern string) bool { return strings.TrimSuffix(pattern, "/") == name })
		default:
			patterns = append(patterns, line)
			count++
		}
		if size > maxIgnoreFileSize || count > maxPatterns {
			return nil, ErrIgnoreFileTooLarge
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return &Matcher{patterns: patterns}, nil
}

// openIgnoreFile opens the .undoignore file of the project at root without
// blocking on a named pipe, and refuses anything but a regular file. A
// symlink is followed only while it stays inside the project: the file's
// lines reach the store and undo status, so it must not be a secret from
// elsewhere, such as a key or another process's environment.
func openIgnoreFile(root string) (*os.File, error) {
	projectRoot, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer projectRoot.Close()
	file, err := projectRoot.OpenFile(IgnoreFile, os.O_RDONLY|unix.O_NONBLOCK, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if err != nil {
		// The root refuses a symlink that leads out of the project.
		info, statErr := projectRoot.Lstat(IgnoreFile)
		if statErr == nil && info.Mode()&fs.ModeSymlink != 0 && !errors.Is(err, fs.ErrPermission) {
			return nil, ErrIgnoreFileOutsideProject
		}
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		return nil, errors.Join(err, file.Close())
	}
	if !info.Mode().IsRegular() {
		return nil, errors.Join(ErrIgnoreFileNotRegular, file.Close())
	}
	return file, nil
}

// FromPatterns returns a Matcher for patterns that a Matcher returned
// earlier, such as the rules recorded with a checkpoint.
func FromPatterns(patterns []string) *Matcher {
	return &Matcher{patterns: slices.Clone(patterns)}
}

// Match reports whether rel is excluded. rel is a slash-separated path
// relative to the project root. A pattern that ends in "/" only matches
// directories. A pattern that contains "/" is matched against the whole path,
// and any other pattern against the last part of it.
func (m *Matcher) Match(rel string, isDir bool) bool {
	base := path.Base(rel)
	for _, pattern := range m.patterns {
		if strings.HasSuffix(pattern, "/") {
			if !isDir {
				continue
			}
			pattern = strings.TrimSuffix(pattern, "/")
		}
		subject := base
		if strings.Contains(pattern, "/") {
			subject = rel
		}
		if ok, _ := path.Match(pattern, subject); ok {
			return true
		}
	}
	return false
}

// Excludes reports whether rel is excluded, either itself or because a
// directory above it is.
func (m *Matcher) Excludes(rel string, isDir bool) bool {
	if rel == "." {
		return false
	}
	for dir := path.Dir(rel); dir != "."; dir = path.Dir(dir) {
		if m.Match(dir, true) {
			return true
		}
	}
	return m.Match(rel, isDir)
}

// Patterns returns the patterns in use.
func (m *Matcher) Patterns() []string {
	return slices.Clone(m.patterns)
}
