package fsnotify

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"unsafe"

	"golang.org/x/sys/unix"
)

const watchMask = unix.IN_CREATE | unix.IN_DELETE | unix.IN_MODIFY | unix.IN_ATTRIB |
	unix.IN_CLOSE_WRITE | unix.IN_MOVED_FROM | unix.IN_MOVED_TO |
	unix.IN_DELETE_SELF | unix.IN_MOVE_SELF | unix.IN_ONLYDIR | unix.IN_DONT_FOLLOW | unix.IN_EXCL_UNLINK

// Stream delivers inotify events for a set of directory trees.
type Stream struct {
	file *os.File
	// fd is the inotify descriptor. file.Fd would switch it back to blocking
	// mode, and Close could then hang in a read.
	fd         int
	roots      []string
	shouldSkip func(dir string) bool
	onEvents   func([]Event)
	done       chan struct{}

	mu           sync.Mutex
	dirsByWatch  map[int]string
	missingRoots map[string]bool
	// hasOverflowed is set when the kernel dropped events, until the next
	// batch reports the rescan.
	hasOverflowed bool
	// hasUnwatchedDirs is set when a new directory could not be watched,
	// usually at the watch limit. Changes there are never reported, so every
	// batch asks for a full comparison.
	hasUnwatchedDirs bool
}

// NewStream watches the directories in paths and everything below them, except
// directories for which shouldSkip returns true, and calls onEvents on a
// background goroutine with each batch of changes. inotify needs a watch per
// directory, so new directories are watched as they appear, and the paths
// already inside them are reported, since they may have been created before
// the watch.
func NewStream(paths []string, shouldSkip func(dir string) bool, onEvents func([]Event)) (*Stream, error) {
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		return nil, err
	}
	if shouldSkip == nil {
		shouldSkip = func(string) bool { return false }
	}
	s := &Stream{
		file:         os.NewFile(uintptr(fd), "inotify"),
		fd:           fd,
		roots:        paths,
		shouldSkip:   shouldSkip,
		onEvents:     onEvents,
		done:         make(chan struct{}),
		dirsByWatch:  map[int]string{},
		missingRoots: map[string]bool{},
	}
	for _, root := range paths {
		if _, err := s.watchTree(root); err != nil {
			s.file.Close()
			return nil, err
		}
	}
	go s.readEvents()
	return s, nil
}

// Close stops the stream. No callback runs after it returns.
func (s *Stream) Close() {
	s.file.Close()
	<-s.done
}

func (s *Stream) readEvents() {
	defer close(s.done)
	buf := make([]byte, 64*1024)
	for {
		n, err := s.file.Read(buf)
		if err != nil {
			return
		}
		if events := s.parseEvents(buf[:n]); len(events) > 0 {
			s.onEvents(events)
		}
	}
}

func (s *Stream) parseEvents(buf []byte) []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	var events []Event
	for len(buf) >= unix.SizeofInotifyEvent {
		raw := (*unix.InotifyEvent)(unsafe.Pointer(&buf[0]))
		end := unix.SizeofInotifyEvent + int(raw.Len)
		name := string(bytes.TrimRight(buf[unix.SizeofInotifyEvent:end], "\x00"))
		buf = buf[end:]

		if raw.Mask&unix.IN_Q_OVERFLOW != 0 {
			s.hasOverflowed = true
			continue
		}
		dir, ok := s.dirsByWatch[int(raw.Wd)]
		if !ok {
			continue
		}
		if raw.Mask&unix.IN_IGNORED != 0 {
			delete(s.dirsByWatch, int(raw.Wd))
			if s.isRoot(dir) {
				s.missingRoots[dir] = true
			}
			continue
		}
		if raw.Mask&(unix.IN_DELETE_SELF|unix.IN_MOVE_SELF) != 0 {
			if s.isRoot(dir) {
				_, _ = unix.InotifyRmWatch(s.fd, uint32(raw.Wd))
				events = append(events, Event{Path: dir, ShouldRescan: true})
			}
			continue
		}
		path := dir
		if name != "" {
			path = filepath.Join(dir, name)
		}
		events = append(events, Event{Path: path})
		if raw.Mask&unix.IN_ISDIR != 0 && raw.Mask&(unix.IN_CREATE|unix.IN_MOVED_TO) != 0 {
			found, err := s.watchTree(path)
			if err != nil {
				s.hasUnwatchedDirs = true
			}
			for _, inside := range found {
				events = append(events, Event{Path: inside})
			}
		}
	}
	for root := range s.missingRoots {
		if _, err := s.watchTree(root); err == nil {
			delete(s.missingRoots, root)
		}
		events = append(events, Event{Path: root, ShouldRescan: true})
	}
	if s.hasOverflowed || s.hasUnwatchedDirs {
		for _, root := range s.roots {
			events = append(events, Event{Path: root, ShouldRescan: true})
		}
		s.hasOverflowed = false
	}
	return events
}

func (s *Stream) isRoot(path string) bool {
	return slices.Contains(s.roots, path)
}

// watchTree adds a watch to dir and every directory below it, and returns
// every path it found inside. Adding a watch to a directory that is already
// watched, such as one that was moved, updates the path it reports.
func (s *Stream) watchTree(dir string) ([]string, error) {
	var found []string
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			if path == dir {
				return err
			}
			return nil
		}
		if path != dir {
			found = append(found, path)
		}
		if !entry.IsDir() {
			return nil
		}
		if path != dir && s.shouldSkip(path) {
			return filepath.SkipDir
		}
		wd, err := unix.InotifyAddWatch(s.fd, path, watchMask)
		if errors.Is(err, unix.ENOSPC) {
			return ErrWatchLimit
		}
		if err != nil {
			if path == dir {
				return fmt.Errorf("watch %s: %w", path, err)
			}
			return nil
		}
		s.dirsByWatch[wd] = path
		return nil
	})
	return found, err
}
