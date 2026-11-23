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
	"time"
)

// Mutator makes random changes to a project, of the kinds agents and their
// tools make: writes in place and through a temporary file, removals,
// renames, exchanges, renames that change only case or Unicode form, files
// and directories that take each other's place, symlinks, hard links, named
// pipes, mode and lock changes, directories moved out and back, and excluded
// directories. Names come from a small set, so changes often hit the same
// paths.
type Mutator struct {
	tb     testing.TB
	random *rand.Rand
	root   string
	// outside is a directory outside the project, for directories moved out
	// and for second names of project files.
	outside string
	options MutatorOptions
}

// MutatorOptions turns on changes that not every way of taking checkpoints
// sees at once.
type MutatorOptions struct {
	// ShouldLinkOutside gives project files second names outside the
	// project and writes through them. The watcher learns of such a name
	// only at its next full comparison, as design.md says.
	ShouldLinkOutside bool
}

// NewMutator returns a Mutator for the project at root, seeded with seed so a
// failure can be replayed.
func NewMutator(tb testing.TB, root string, seed uint64, options MutatorOptions) *Mutator {
	tb.Helper()
	outside := tb.TempDir()
	tb.Cleanup(func() {
		makeTreeWritable(root)
		makeTreeWritable(outside)
	})
	return &Mutator{
		tb:      tb,
		random:  rand.New(rand.NewPCG(seed, seed)),
		root:    root,
		outside: outside,
		options: options,
	}
}

// Mutate makes one random change and describes it. A change the filesystem
// refuses, such as a rename onto a directory that is not empty, is skipped.
func (m *Mutator) Mutate() string {
	rel := m.randomPath()
	if m.isThroughSymlink(rel) {
		return "skip " + rel
	}
	changes := []func(rel, path string) string{
		m.write, m.write, m.write, m.writeAtomically, m.writeKeepingTimes,
		m.makeDir, m.remove, m.rename, m.exchange,
		m.renameToUpperCase, m.renameToLowerCase, m.renameToOtherForm,
		m.symlink, m.hardLink, m.makeFifo, m.chmod, m.toggleLock,
		m.moveOutAndBack, m.linkOutside, m.writeOutside,
	}
	return changes[m.random.IntN(len(changes))](rel, filepath.Join(m.root, rel))
}

func (m *Mutator) randomContent(maxSize int) string {
	return strings.Repeat(string(rune('a'+m.random.IntN(26))), m.random.IntN(maxSize))
}

func (m *Mutator) write(rel, path string) string {
	content := m.randomContent(5000)
	// Opening a named pipe to write waits for a reader forever.
	if info, err := os.Stat(path); err == nil && !info.Mode().IsRegular() {
		return "skip write " + rel
	}
	if os.MkdirAll(filepath.Dir(path), 0o755) == nil {
		_ = os.WriteFile(path, []byte(content), 0o644)
	}
	return fmt.Sprintf("write %s (%d bytes)", rel, len(content))
}

// writeAtomically writes a temporary file and renames it over path, as
// editors save.
func (m *Mutator) writeAtomically(rel, path string) string {
	content := m.randomContent(5000)
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return "skip atomic write " + rel
	}
	temporary := filepath.Join(filepath.Dir(path), ".tmp-write")
	if os.MkdirAll(filepath.Dir(path), 0o755) == nil && os.WriteFile(temporary, []byte(content), 0o600) == nil {
		_ = os.Rename(temporary, path)
	}
	return fmt.Sprintf("atomic write %s (%d bytes)", rel, len(content))
}

// writeKeepingTimes changes one byte of a file and puts its modification time
// back, so only its change time shows the write.
func (m *Mutator) writeKeepingTimes(rel, path string) string {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return "skip write keeping times " + rel
	}
	file, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return "skip write keeping times " + rel
	}
	_, _ = file.WriteAt([]byte{byte('A' + m.random.IntN(26))}, m.random.Int64N(info.Size()))
	_ = file.Close()
	_ = os.Chtimes(path, info.ModTime(), info.ModTime())
	return "write keeping times " + rel
}

func (m *Mutator) makeDir(rel, path string) string {
	_ = os.MkdirAll(path, 0o755)
	return "mkdir " + rel
}

func (m *Mutator) remove(rel, path string) string {
	makeTreeWritable(path)
	_ = os.RemoveAll(path)
	return "remove " + rel
}

func (m *Mutator) rename(rel, path string) string {
	to := m.randomPath()
	if m.isThroughSymlink(to) {
		return "skip " + to
	}
	_ = os.Rename(path, filepath.Join(m.root, to))
	return fmt.Sprintf("rename %s %s", rel, to)
}

// exchange swaps two paths in one step, as some tools save.
func (m *Mutator) exchange(rel, path string) string {
	to := m.randomPath()
	if m.isThroughSymlink(to) {
		return "skip " + to
	}
	if err := exchangePaths(path, filepath.Join(m.root, to)); err != nil {
		return fmt.Sprintf("skip exchange %s %s", rel, to)
	}
	return fmt.Sprintf("exchange %s %s", rel, to)
}

// renameToUpperCase and renameToLowerCase skip the excluded name: in another
// case it is a different name to the exclude rules but the same folder to a
// filesystem that ignores case, which undo does not reconcile.
func (m *Mutator) renameToUpperCase(rel, path string) string {
	if filepath.Base(path) == excludedDirName {
		return "skip rename to upper case " + rel
	}
	_ = os.Rename(path, filepath.Join(filepath.Dir(path), strings.ToUpper(filepath.Base(path))))
	return "rename to upper case " + rel
}

func (m *Mutator) renameToLowerCase(rel, path string) string {
	if strings.EqualFold(filepath.Base(path), excludedDirName) {
		return "skip rename to lower case " + rel
	}
	_ = os.Rename(path, filepath.Join(filepath.Dir(path), strings.ToLower(filepath.Base(path))))
	return "rename to lower case " + rel
}

// renameToOtherForm renames between the composed and decomposed forms of
// the same accented name.
func (m *Mutator) renameToOtherForm(rel, path string) string {
	otherForms := map[string]string{composedE: decomposedE, decomposedE: composedE, composedUpperE: decomposedUpperE}
	other, ok := otherForms[filepath.Base(path)]
	if !ok {
		return "skip rename to other form " + rel
	}
	_ = os.Rename(path, filepath.Join(filepath.Dir(path), other))
	return fmt.Sprintf("rename to other form %+q", rel)
}

func (m *Mutator) symlink(rel, path string) string {
	targets := []string{"a", "b", "c/a", "missing", ".", "..", "../..", "/etc"}
	target := targets[m.random.IntN(len(targets))]
	if os.MkdirAll(filepath.Dir(path), 0o755) == nil {
		_ = os.Symlink(target, path)
	}
	return fmt.Sprintf("symlink %s -> %s", rel, target)
}

func (m *Mutator) hardLink(rel, path string) string {
	// Tools link regular files. FSEvents reports nothing for a new name of a
	// named pipe, which has no content to restore.
	from := m.randomPath()
	if info, err := os.Lstat(filepath.Join(m.root, from)); err != nil || !info.Mode().IsRegular() || m.isThroughSymlink(from) {
		return "skip hard link " + from
	}
	// Linux watches no excluded directory, so a name made there is seen
	// only at the next full comparison, as design.md says.
	if runtime.GOOS == "linux" && slices.Contains(strings.Split(rel, string(filepath.Separator)), excludedDirName) {
		return "skip hard link into " + rel
	}
	_ = os.Link(filepath.Join(m.root, from), path)
	return fmt.Sprintf("hard link %s %s", from, rel)
}

func (m *Mutator) makeFifo(rel, path string) string {
	if os.MkdirAll(filepath.Dir(path), 0o755) == nil {
		_ = syscall.Mkfifo(path, 0o644)
	}
	return "fifo " + rel
}

func (m *Mutator) chmod(rel, path string) string {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&fs.ModeSymlink != 0 {
		return "skip chmod " + rel
	}
	modes := []fs.FileMode{0o600, 0o644, 0o755, 0o555, 0o000, 0o200, 0o4755, 0o2644}
	// Directories keep search permission: undo leaves a directory it cannot
	// read as it is, which other tests cover.
	if info.IsDir() {
		modes = []fs.FileMode{0o700, 0o755, 0o555}
	}
	mode := modes[m.random.IntN(len(modes))]
	_ = os.Chmod(path, mode)
	return fmt.Sprintf("chmod %s %v", rel, mode)
}

// toggleLock locks path, or now and then unlocks it, where the system has
// file locks.
func (m *Mutator) toggleLock(rel, path string) string {
	isLocked := m.random.IntN(3) != 0
	if !setLocked(path, isLocked) {
		return "skip lock " + rel
	}
	return fmt.Sprintf("set locked %s %v", rel, isLocked)
}

// moveOutAndBack moves a directory out of the project, writes a file in it
// and moves it back, so the file has no event inside the project.
func (m *Mutator) moveOutAndBack(rel, path string) string {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() {
		return "skip move out " + rel
	}
	away := filepath.Join(m.outside, fmt.Sprintf("dir-%d", m.random.IntN(1_000_000)))
	if os.Rename(path, away) != nil {
		return "skip move out " + rel
	}
	_ = os.WriteFile(filepath.Join(away, "moved.txt"), []byte(m.randomContent(100)), 0o644)
	time.Sleep(10 * time.Millisecond)
	_ = os.Rename(away, path)
	return "move out, write and move back " + rel
}

// linkOutside gives a project file a second name outside the project.
func (m *Mutator) linkOutside(rel, path string) string {
	info, err := os.Lstat(path)
	if !m.options.ShouldLinkOutside || err != nil || !info.Mode().IsRegular() {
		return "skip link outside " + rel
	}
	name := filepath.Join(m.outside, fmt.Sprintf("file-%d", m.random.IntN(1_000_000)))
	if os.Link(path, name) != nil {
		return "skip link outside " + rel
	}
	return fmt.Sprintf("link outside %s %s", rel, filepath.Base(name))
}

// writeOutside writes through a name outside the project, which the project
// may share with one of its files.
func (m *Mutator) writeOutside(string, string) string {
	entries, err := os.ReadDir(m.outside)
	if !m.options.ShouldLinkOutside || err != nil || len(entries) == 0 {
		return "skip write outside"
	}
	entry := entries[m.random.IntN(len(entries))]
	if !entry.Type().IsRegular() {
		return "skip write outside " + entry.Name()
	}
	content := m.randomContent(3000)
	file, err := os.OpenFile(filepath.Join(m.outside, entry.Name()), os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return "skip write outside " + entry.Name()
	}
	_, _ = file.WriteString(content)
	_ = file.Close()
	return fmt.Sprintf("write outside %s (%d bytes)", entry.Name(), len(content))
}

// The accented names come in both Unicode forms, which a filesystem that
// ignores Unicode form treats as the same name.
const (
	composedE        = "é"
	decomposedE      = "é"
	composedUpperE   = "É"
	decomposedUpperE = "É"
)

func (m *Mutator) randomPath() string {
	names := []string{"a", "b", "c", "A", excludedDirName, composedE, decomposedE, composedUpperE}
	parts := make([]string, 1+m.random.IntN(3))
	for i := range parts {
		parts[i] = names[m.random.IntN(len(names))]
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

// makeTreeWritable unlocks everything under path and gives the user write
// and search permission on every directory, so the tree can be changed and
// removed.
func makeTreeWritable(path string) {
	_ = filepath.WalkDir(path, func(entryPath string, entry fs.DirEntry, err error) error {
		if err != nil || entry.Type()&fs.ModeSymlink != 0 {
			return nil //nolint:nilerr // a path that cannot be reached cannot block the removal either
		}
		_ = setLocked(entryPath, false)
		if entry.IsDir() {
			_ = os.Chmod(entryPath, 0o755)
		}
		return nil
	})
}
