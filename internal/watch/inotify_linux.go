package watch

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"unsafe"

	"golang.org/x/sys/unix"
)

const watchMask = unix.IN_CREATE | unix.IN_DELETE | unix.IN_MODIFY | unix.IN_ATTRIB |
	unix.IN_CLOSE_WRITE | unix.IN_MOVED_FROM | unix.IN_MOVED_TO |
	unix.IN_DELETE_SELF | unix.IN_MOVE_SELF | unix.IN_ONLYDIR | unix.IN_DONT_FOLLOW | unix.IN_EXCL_UNLINK

// ErrWatchLimit means the system ran out of inotify watches.
var ErrWatchLimit = errors.New("inotify watch limit reached; raise it with: sudo sysctl fs.inotify.max_user_watches=1048576")

// Stream delivers inotify events for a set of directory trees.
type Stream struct {
	file     *os.File
	fd       int
	roots    []string
	skip     func(dir string) bool
	onEvents func([]Event)
	done     chan struct{}

	mu      sync.Mutex
	paths   map[int]string
	missing map[string]bool
	full    bool
}

// Start watches the directories in paths and everything below them, except
// directories for which skip returns true, and calls onEvents on a background
// goroutine with each batch of changes. inotify needs a watch per directory,
// so new directories are watched as they appear, and the paths already inside
// them are reported, since they may have been created before the watch.
func Start(paths []string, skip func(dir string) bool, onEvents func([]Event)) (*Stream, error) {
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		return nil, err
	}
	if skip == nil {
		skip = func(string) bool { return false }
	}
	s := &Stream{
		file:     os.NewFile(uintptr(fd), "inotify"),
		fd:       fd,
		roots:    paths,
		skip:     skip,
		onEvents: onEvents,
		done:     make(chan struct{}),
		paths:    map[int]string{},
		missing:  map[string]bool{},
	}
	for _, root := range paths {
		if _, err := s.watchTree(root); err != nil {
			s.file.Close()
			return nil, err
		}
	}
	go s.read()
	return s, nil
}

// Close stops the stream. No callback runs after it returns.
func (s *Stream) Close() {
	s.file.Close()
	<-s.done
}

func (s *Stream) read() {
	defer close(s.done)
	buf := make([]byte, 64*1024)
	for {
		n, err := s.file.Read(buf)
		if err != nil {
			return
		}
		if events := s.parse(buf[:n]); len(events) > 0 {
			s.onEvents(events)
		}
	}
}

func (s *Stream) parse(buf []byte) []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	var events []Event
	for len(buf) >= unix.SizeofInotifyEvent {
		raw := (*unix.InotifyEvent)(unsafe.Pointer(&buf[0]))
		end := unix.SizeofInotifyEvent + int(raw.Len)
		name := string(bytes.TrimRight(buf[unix.SizeofInotifyEvent:end], "\x00"))
		buf = buf[end:]

		if raw.Mask&unix.IN_Q_OVERFLOW != 0 {
			s.full = true
			continue
		}
		dir, ok := s.paths[int(raw.Wd)]
		if !ok {
			continue
		}
		if raw.Mask&unix.IN_IGNORED != 0 {
			delete(s.paths, int(raw.Wd))
			if s.isRoot(dir) {
				s.missing[dir] = true
			}
			continue
		}
		if raw.Mask&(unix.IN_DELETE_SELF|unix.IN_MOVE_SELF) != 0 {
			if s.isRoot(dir) {
				_, _ = unix.InotifyRmWatch(s.fd, uint32(raw.Wd))
				events = append(events, Event{Path: dir, Rescan: true})
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
				s.full = true
			}
			for _, inside := range found {
				events = append(events, Event{Path: inside})
			}
		}
	}
	for root := range s.missing {
		if _, err := s.watchTree(root); err == nil {
			delete(s.missing, root)
		}
		events = append(events, Event{Path: root, Rescan: true})
	}
	if s.full {
		for _, root := range s.roots {
			events = append(events, Event{Path: root, Rescan: true})
		}
	}
	return events
}

func (s *Stream) isRoot(path string) bool {
	for _, root := range s.roots {
		if root == path {
			return true
		}
	}
	return false
}

// watchTree adds a watch to dir and every directory below it, and returns
// every path it found inside. Adding a watch to a directory that is already
// watched, such as one that was moved, updates the path it reports.
func (s *Stream) watchTree(dir string) ([]string, error) {
	var found []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == dir {
				return err
			}
			return nil
		}
		if path != dir {
			found = append(found, path)
		}
		if !d.IsDir() {
			return nil
		}
		if path != dir && s.skip(path) {
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
		s.paths[wd] = path
		return nil
	})
	return found, err
}
