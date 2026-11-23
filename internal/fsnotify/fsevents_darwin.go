package fsnotify

/*
#cgo LDFLAGS: -framework CoreServices
#include <CoreServices/CoreServices.h>
#include <dispatch/dispatch.h>

extern void undoStreamEvents(uintptr_t handle, size_t count, char **paths, FSEventStreamEventFlags *flags,
                             FSEventStreamEventId *ids);

static void undoStreamCallback(ConstFSEventStreamRef stream, void *info, size_t count, void *paths,
                               const FSEventStreamEventFlags flags[], const FSEventStreamEventId ids[]) {
	undoStreamEvents((uintptr_t)info, count, (char **)paths, (FSEventStreamEventFlags *)flags,
	                 (FSEventStreamEventId *)ids);
}

static FSEventStreamRef undoStreamCreate(uintptr_t handle, char **paths, int count, FSEventStreamEventId since) {
	CFMutableArrayRef roots = CFArrayCreateMutable(NULL, count, &kCFTypeArrayCallBacks);
	for (int i = 0; i < count; i++) {
		CFStringRef root = CFStringCreateWithCString(NULL, paths[i], kCFStringEncodingUTF8);
		CFArrayAppendValue(roots, root);
		CFRelease(root);
	}
	FSEventStreamContext context = {0, (void *)handle, NULL, NULL, NULL};
	FSEventStreamRef stream = FSEventStreamCreate(NULL, undoStreamCallback, &context, roots,
		since == 0 ? kFSEventStreamEventIdSinceNow : since, 0.0,
		kFSEventStreamCreateFlagFileEvents | kFSEventStreamCreateFlagNoDefer | kFSEventStreamCreateFlagWatchRoot);
	CFRelease(roots);
	return stream;
}

// undoEventStoreID writes the UUID of the FSEvents history of the volume
// that holds dev. A volume whose history was reset gets a new one.
static int undoEventStoreID(dev_t dev, char *buffer, CFIndex size) {
	CFUUIDRef uuid = FSEventsCopyUUIDForDevice(dev);
	if (uuid == NULL) {
		return -1;
	}
	CFStringRef text = CFUUIDCreateString(NULL, uuid);
	CFRelease(uuid);
	Boolean isConverted = CFStringGetCString(text, buffer, size, kCFStringEncodingUTF8);
	CFRelease(text);
	return isConverted ? 0 : -1;
}

static dispatch_queue_t undoQueueCreate(void) {
	return dispatch_queue_create("undo.fsnotify", DISPATCH_QUEUE_SERIAL);
}

static int undoStreamStart(FSEventStreamRef stream, dispatch_queue_t queue) {
	FSEventStreamSetDispatchQueue(stream, queue);
	return FSEventStreamStart(stream);
}

static void undoDrained(void *context) {}

// Callbacks that are already queued still run after the stream is
// invalidated. Waiting for the queue makes sure none are left before the
// handle is freed.
static void undoStreamStop(FSEventStreamRef stream, dispatch_queue_t queue) {
	FSEventStreamStop(stream);
	FSEventStreamInvalidate(stream);
	dispatch_sync_f(queue, NULL, undoDrained);
	FSEventStreamRelease(stream);
	dispatch_release(queue);
}
*/
import "C"

import (
	"errors"
	"os"
	"runtime/cgo"
	"sync"
	"syscall"
	"unsafe"
)

const rescanFlags = C.kFSEventStreamEventFlagMustScanSubDirs |
	C.kFSEventStreamEventFlagUserDropped |
	C.kFSEventStreamEventFlagKernelDropped |
	C.kFSEventStreamEventFlagRootChanged |
	C.kFSEventStreamEventFlagEventIdsWrapped

// itemKindFlags say what kind of item an event is about, not what happened.
const itemKindFlags = C.kFSEventStreamEventFlagItemIsFile |
	C.kFSEventStreamEventFlagItemIsDir |
	C.kFSEventStreamEventFlagItemIsSymlink |
	C.kFSEventStreamEventFlagItemIsHardlink |
	C.kFSEventStreamEventFlagItemIsLastHardlink

// CanResume reports whether a stream can start from an earlier EventID.
const CanResume = true

// Stream delivers FSEvents for a set of directories.
type Stream struct {
	ref       C.FSEventStreamRef
	queue     C.dispatch_queue_t
	handle    cgo.Handle
	closeOnce sync.Once
}

// NewStream watches the directories in paths and calls onEvents on a
// background thread with each batch of changes. With since other than
// SinceNow, it first delivers the changes made after event since, as long
// as the volume's history has them. FSEvents watches whole trees, so
// shouldSkip, which tells the Linux stream what not to watch, is not used.
func NewStream(paths []string, since EventID, _ func(dir string) bool, onEvents func([]Event)) (*Stream, error) {
	cPaths := make([]*C.char, len(paths))
	for i, path := range paths {
		cPaths[i] = C.CString(path)
		defer C.free(unsafe.Pointer(cPaths[i]))
	}
	handle := cgo.NewHandle(onEvents)
	ref := C.undoStreamCreate(C.uintptr_t(handle), &cPaths[0], C.int(len(cPaths)), C.FSEventStreamEventId(since))
	if ref == nil {
		handle.Delete()
		return nil, errors.New("FSEventStreamCreate failed")
	}
	queue := C.undoQueueCreate()
	if C.undoStreamStart(ref, queue) == 0 {
		C.undoStreamStop(ref, queue)
		handle.Delete()
		return nil, errors.New("FSEventStreamStart failed")
	}
	return &Stream{ref: ref, queue: queue, handle: handle}, nil
}

// Close stops the stream. Closing it again does nothing.
func (s *Stream) Close() {
	s.closeOnce.Do(func() {
		C.undoStreamStop(s.ref, s.queue)
		s.handle.Delete()
	})
}

// EventStoreID identifies the FSEvents history of the volume that holds
// path. Event IDs from one history mean nothing in another, so a stream can
// only resume while it is the same.
func EventStoreID(path string) (string, error) {
	var stat syscall.Stat_t
	if err := syscall.Stat(path, &stat); err != nil {
		return "", &os.PathError{Op: "stat", Path: path, Err: err}
	}
	var buffer [64]C.char
	if C.undoEventStoreID(C.dev_t(stat.Dev), &buffer[0], C.CFIndex(len(buffer))) != 0 {
		return "", errors.New("FSEventsCopyUUIDForDevice failed")
	}
	return C.GoString(&buffer[0]), nil
}

//export undoStreamEvents
func undoStreamEvents(handle C.uintptr_t, count C.size_t, paths **C.char, flags *C.FSEventStreamEventFlags,
	ids *C.FSEventStreamEventId) {
	onEvents := cgo.Handle(handle).Value().(func([]Event))
	pathList := unsafe.Slice(paths, count)
	flagList := unsafe.Slice(flags, count)
	idList := unsafe.Slice(ids, count)
	events := make([]Event, 0, count)
	for i := range count {
		// Cloning a file reports an event on the source, whose content and
		// times do not change. A checkpoint clones every changed file, so
		// these would make the next checkpoint check them all again.
		if flagList[i]&^itemKindFlags == C.kFSEventStreamEventFlagItemCloned {
			continue
		}
		if flagList[i]&C.kFSEventStreamEventFlagHistoryDone != 0 {
			events = append(events, Event{ID: EventID(idList[i]), IsHistoryDone: true})
			continue
		}
		events = append(events, Event{
			Path:         C.GoString(pathList[i]),
			ID:           EventID(idList[i]),
			ShouldRescan: flagList[i]&rescanFlags != 0,
			IsRenamed:    flagList[i]&C.kFSEventStreamEventFlagItemRenamed != 0,
		})
	}
	onEvents(events)
}
