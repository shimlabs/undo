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
