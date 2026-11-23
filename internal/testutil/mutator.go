package testutil

import (
	"fmt"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
)

// Mutator makes random changes to a project, of the kinds agents and their
// tools make: writes, removals, renames that change only case, files and
// directories that take each other's place, symlinks, hard links, named
// pipes, mode changes and excluded directories. Names come from a small set,
// so changes often hit the same paths.
type Mutator struct {
	tb   testing.TB
	rng  *rand.Rand
	root string
}

// NewMutator returns a Mutator for the project at root, seeded with seed so a
// failure can be replayed.
func NewMutator(tb testing.TB, root string, seed uint64) *Mutator {
	tb.Helper()
	tb.Cleanup(func() { makeTreeWritable(root) })
	return &Mutator{tb: tb, rng: rand.New(rand.NewPCG(seed, seed)), root: root}
}

// Mutate makes one random change and describes it. A change the filesystem
// refuses, such as a rename onto a directory that is not empty, is skipped.
func (m *Mutator) Mutate() string {
	rel := m.randomPath()
	path := filepath.Join(m.root, rel)
	if m.isThroughSymlink(rel) {
		return "skip " + rel
	}
	switch m.rng.IntN(11) {
	case 0, 1, 2:
		content := strings.Repeat(string(rune('a'+m.rng.IntN(26))), m.rng.IntN(5000))
		// Opening a named pipe to write waits for a reader forever.
		if info, err := os.Stat(path); err == nil && !info.Mode().IsRegular() {
			return "skip write " + rel
		}
		if os.MkdirAll(filepath.Dir(path), 0o755) == nil {
			_ = os.WriteFile(path, []byte(content), 0o644)
		}
		return fmt.Sprintf("write %s (%d bytes)", rel, len(content))
	case 3:
		_ = os.MkdirAll(path, 0o755)
		return "mkdir " + rel
	case 4:
		makeTreeWritable(path)
		_ = os.RemoveAll(path)
		return "remove " + rel
	case 5:
		to := m.randomPath()
		if m.isThroughSymlink(to) {
			return "skip " + to
		}
		_ = os.Rename(path, filepath.Join(m.root, to))
		return fmt.Sprintf("rename %s %s", rel, to)
	case 6:
		// An excluded name in another case is a different name to the
		// exclude rules but the same folder to a filesystem that ignores
		// case, which undo does not reconcile.
		if filepath.Base(path) == "node_modules" {
			return "skip rename to upper case " + rel
		}
		upper := filepath.Join(filepath.Dir(path), strings.ToUpper(filepath.Base(path)))
		_ = os.Rename(path, upper)
		return "rename to upper case " + rel
	case 7:
		target := []string{"a", "b", "c/a", "missing"}[m.rng.IntN(4)]
		if os.MkdirAll(filepath.Dir(path), 0o755) == nil {
			_ = os.Symlink(target, path)
		}
		return fmt.Sprintf("symlink %s -> %s", rel, target)
	case 8:
		// Tools link regular files. FSEvents reports nothing for a new name
		// of a named pipe, which has no content to restore.
		from := m.randomPath()
		if info, err := os.Lstat(filepath.Join(m.root, from)); err != nil || !info.Mode().IsRegular() || m.isThroughSymlink(from) {
			return "skip hard link " + from
		}
		// Linux watches no excluded directory, so a name made there is seen
		// only at the next full comparison, as design.md says.
		if runtime.GOOS == "linux" && slices.Contains(strings.Split(rel, string(filepath.Separator)), "node_modules") {
			return "skip hard link into " + rel
		}
		_ = os.Link(filepath.Join(m.root, from), path)
		return fmt.Sprintf("hard link %s %s", from, rel)
	case 9:
		if os.MkdirAll(filepath.Dir(path), 0o755) == nil {
			_ = syscall.Mkfifo(path, 0o644)
		}
		return "fifo " + rel
	default:
		// Directories keep search permission: undo leaves a directory it
		// cannot read as it is, which other tests cover.
		modes := []fs.FileMode{0o600, 0o644, 0o755, 0o555}
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&fs.ModeSymlink != 0 {
			return "skip chmod " + rel
		}
		if info.IsDir() {
			modes = []fs.FileMode{0o700, 0o755, 0o555}
		}
		mode := modes[m.rng.IntN(len(modes))]
		_ = os.Chmod(path, mode)
		return fmt.Sprintf("chmod %s %v", rel, mode)
	}
}

func (m *Mutator) randomPath() string {
	names := []string{"a", "b", "c", "A", "node_modules"}
	parts := make([]string, 1+m.rng.IntN(3))
	for i := range parts {
		parts[i] = names[m.rng.IntN(len(names))]
	}
	return filepath.Join(parts...)
}

// isThroughSymlink reports whether a parent of rel is a symlink or not a
// directory, so a change to rel would land somewhere else.
func (m *Mutator) isThroughSymlink(rel string) bool {
	dir := m.root
	parts := strings.Split(rel, string(filepath.Separator))
	for _, part := range parts[:len(parts)-1] {
		dir = filepath.Join(dir, part)
		info, err := os.Lstat(dir)
		if err == nil && !info.IsDir() {
			return true
		}
	}
	return false
}

// makeTreeWritable gives the user write and search permission on every
// directory under path, so the tree can be changed and removed.
func makeTreeWritable(path string) {
	_ = filepath.WalkDir(path, func(entryPath string, entry fs.DirEntry, err error) error {
		if err == nil && entry.IsDir() {
			_ = os.Chmod(entryPath, 0o755)
		}
		return nil
	})
}

// Snapshot describes every path under root that undo restores: its kind,
// permissions, symlink target, and for a file its modification time and
// content. Paths inside an excluded node_modules directory are left out,
// since a restore never touches them, and so are directory times, which an
// excluded change also moves.
func Snapshot(tb testing.TB, root string) map[string]string {
	tb.Helper()
	entries := map[string]string{}
	err := filepath.WalkDir(root, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if slices.Contains(strings.Split(rel, string(filepath.Separator)), "node_modules") {
			return nil
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		entries[rel], err = describe(path, info)
		return err
	})
	if err != nil {
		tb.Fatal(err)
	}
	return entries
}

func describe(path string, info fs.FileInfo) (string, error) {
	switch {
	case info.Mode()&fs.ModeSymlink != 0:
		target, err := os.Readlink(path)
		return "link " + target, err
	case info.Mode()&fs.ModeNamedPipe != 0:
		return fmt.Sprintf("fifo %v", info.Mode().Perm()), nil
	case info.IsDir():
		return fmt.Sprintf("dir %v", info.Mode().Perm()), nil
	default:
		data, err := os.ReadFile(path)
		return fmt.Sprintf("file %v %d %q", info.Mode().Perm(), info.ModTime().UnixNano(), data), err
	}
}

// SnapshotDiff lists the paths whose description differs between want and
// got. A directory got has and want lacks is not listed when it holds an
// excluded node_modules, which a restore must leave in place.
func SnapshotDiff(want, got map[string]string) []string {
	var paths []string
	for rel, description := range want {
		if got[rel] != description {
			paths = append(paths, rel)
		}
	}
	for rel, description := range got {
		if _, isWanted := want[rel]; isWanted {
			continue
		}
		if !strings.HasPrefix(description, "dir ") || !holdsExcluded(got, rel) {
			paths = append(paths, rel)
		}
	}
	slices.Sort(paths)
	return paths
}

// holdsExcluded reports whether the directory rel in snapshot has nothing in
// it but directories, which then must lead to excluded content.
func holdsExcluded(snapshot map[string]string, rel string) bool {
	prefix := rel + string(filepath.Separator)
	for other, description := range snapshot {
		if strings.HasPrefix(other, prefix) && !strings.HasPrefix(description, "dir ") {
			return false
		}
	}
	return true
}
