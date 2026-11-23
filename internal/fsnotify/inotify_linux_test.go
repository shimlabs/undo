package fsnotify

import (
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

// inotifyRecord encodes one inotify event the way the kernel does, without a
// name.
func inotifyRecord(wd int32, mask uint32) []byte {
	event := unix.InotifyEvent{Wd: wd, Mask: mask}
	return unsafe.Slice((*byte)(unsafe.Pointer(&event)), unix.SizeofInotifyEvent)
}

func hasRescan(events []Event) bool {
	for _, event := range events {
		if event.ShouldRescan {
			return true
		}
	}
	return false
}

func TestOverflowAsksForOneRescan(t *testing.T) {
	stream := &Stream{
		roots:        []string{"/project"},
		dirsByWatch:  map[int]string{1: "/project"},
		missingRoots: map[string]bool{},
	}

	if !hasRescan(stream.parseEvents(inotifyRecord(-1, unix.IN_Q_OVERFLOW))) {
		t.Fatal("an overflow did not ask for a rescan")
	}
	if hasRescan(stream.parseEvents(inotifyRecord(1, unix.IN_MODIFY))) {
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
	created := append(inotifyRecord(1, unix.IN_CREATE|unix.IN_ISDIR), nameBytes("gone")...)
	created[unix.SizeofInotifyEvent-4] = byte(len(nameBytes("gone")))

	stream.parseEvents(created)
	if hasRescan(stream.parseEvents(inotifyRecord(1, unix.IN_MODIFY))) {
		t.Fatal("a directory that vanished before it was watched made every batch ask for a rescan")
	}
}

// nameBytes pads name to inotify's alignment, as the kernel does.
func nameBytes(name string) []byte {
	padded := make([]byte, 16)
	copy(padded, name)
	return padded
}
