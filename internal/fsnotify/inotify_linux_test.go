package fsnotify

import (
	"slices"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

// inotifyRecord encodes one inotify event the way the kernel does, with name
// padded to inotify's alignment.
func inotifyRecord(wd int32, mask uint32, name string) []byte {
	var padded []byte
	if name != "" {
		padded = make([]byte, (len(name)/16+1)*16)
		copy(padded, name)
	}
	event := unix.InotifyEvent{Wd: wd, Mask: mask, Len: uint32(len(padded))}
	header := unsafe.Slice((*byte)(unsafe.Pointer(&event)), unix.SizeofInotifyEvent)
	return append(slices.Clone(header), padded...)
}

func hasRescan(events []Event) bool {
	return slices.ContainsFunc(events, func(event Event) bool { return event.ShouldRescan })
}

func TestOverflowAsksForOneRescan(t *testing.T) {
	stream := &Stream{
		roots:        []string{"/project"},
		dirsByWatch:  map[int]string{1: "/project"},
		missingRoots: map[string]bool{},
	}

	if !hasRescan(stream.parseEvents(inotifyRecord(-1, unix.IN_Q_OVERFLOW, ""))) {
		t.Fatal("an overflow did not ask for a rescan")
	}
	if hasRescan(stream.parseEvents(inotifyRecord(1, unix.IN_MODIFY, ""))) {
		t.Fatal("a later batch still asked for a rescan after the overflow was reported")
	}
}

func TestVanishedDirectoryDoesNotForceRescans(t *testing.T) {
	root := t.TempDir()
	stream := &Stream{
		roots:        []string{root},
		dirsByWatch:  map[int]string{1: root},
		missingRoots: map[string]bool{},
	}
	created := inotifyRecord(1, unix.IN_CREATE|unix.IN_ISDIR, "gone")

	stream.parseEvents(created)
	if hasRescan(stream.parseEvents(inotifyRecord(1, unix.IN_MODIFY, ""))) {
		t.Fatal("a directory that vanished before it was watched made every batch ask for a rescan")
	}
}
