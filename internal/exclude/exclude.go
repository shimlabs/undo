// Package exclude decides which paths undo leaves out of checkpoints.
package exclude

import (
	"bufio"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// Defaults lists directories that tools rebuild on their own. They are
// skipped unless .undoignore brings one back with a "!" line.
var Defaults = []string{
	"node_modules", ".venv", "venv", "__pycache__", "target", ".next", ".turbo", ".gradle", ".cache",
}

// Matcher checks project paths against the exclude patterns.
type Matcher struct {
	patterns []string
}

// New returns a Matcher with the defaults plus the patterns in the project's
// .undoignore file.
func New(root string) (*Matcher, error) {
	patterns := slices.Clone(Defaults)
	file, err := os.Open(filepath.Join(root, ".undoignore"))
	if errors.Is(err, fs.ErrNotExist) {
		return &Matcher{patterns: patterns}, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch {
		case line == "" || strings.HasPrefix(line, "#"):
		case strings.HasPrefix(line, "!"):
			patterns = slices.DeleteFunc(patterns, func(p string) bool { return p == line[1:] })
		default:
			patterns = append(patterns, line)
		}
	}
	return &Matcher{patterns: patterns}, scanner.Err()
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

// Patterns returns the patterns in use.
func (m *Matcher) Patterns() []string {
	return slices.Clone(m.patterns)
}
