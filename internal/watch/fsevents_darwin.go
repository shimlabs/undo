package watch

/*
#cgo LDFLAGS: -framework CoreServices
#include <CoreServices/CoreServices.h>
#include <dispatch/dispatch.h>

extern void undoStreamEvents(uintptr_t handle, size_t count, char **paths, FSEventStreamEventFlags *flags);

static void undoStreamCallback(ConstFSEventStreamRef stream, void *info, size_t count, void *paths,
                               const FSEventStreamEventFlags flags[], const FSEventStreamEventId ids[]) {
	undoStreamEvents((uintptr_t)info, count, (char **)paths, (FSEventStreamEventFlags *)flags);
}

static FSEventStreamRef undoStreamCreate(uintptr_t handle, char **paths, int count) {
	CFMutableArrayRef roots = CFArrayCreateMutable(NULL, count, &kCFTypeArrayCallBacks);
	for (int i = 0; i < count; i++) {
		CFStringRef root = CFStringCreateWithCString(NULL, paths[i], kCFStringEncodingUTF8);
		CFArrayAppendValue(roots, root);
		CFRelease(root);
	}
	FSEventStreamContext context = {0, (void *)handle, NULL, NULL, NULL};
	FSEventStreamRef stream = FSEventStreamCreate(NULL, undoStreamCallback, &context, roots,
		kFSEventStreamEventIdSinceNow, 0.0,
		kFSEventStreamCreateFlagFileEvents | kFSEventStreamCreateFlagNoDefer | kFSEventStreamCreateFlagWatchRoot);
	CFRelease(roots);
	return stream;
}

static dispatch_queue_t undoQueueCreate(void) {
	return dispatch_queue_create("undo.watch", DISPATCH_QUEUE_SERIAL);
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
	"runtime/cgo"
	"unsafe"
)

const rescanFlags = C.kFSEventStreamEventFlagMustScanSubDirs |
	C.kFSEventStreamEventFlagUserDropped |
	C.kFSEventStreamEventFlagKernelDropped |
	C.kFSEventStreamEventFlagRootChanged

// Stream delivers FSEvents for a set of directories.
type Stream struct {
	ref    C.FSEventStreamRef
	queue  C.dispatch_queue_t
	handle cgo.Handle
}

// Start watches the directories in paths and calls onEvents on a background
// thread with each batch of changes.
func Start(paths []string, onEvents func([]Event)) (*Stream, error) {
	cPaths := make([]*C.char, len(paths))
	for i, path := range paths {
		cPaths[i] = C.CString(path)
		defer C.free(unsafe.Pointer(cPaths[i]))
	}
	handle := cgo.NewHandle(onEvents)
	ref := C.undoStreamCreate(C.uintptr_t(handle), &cPaths[0], C.int(len(cPaths)))
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

// Close stops the stream.
func (s *Stream) Close() {
	C.undoStreamStop(s.ref, s.queue)
	s.handle.Delete()
}

//export undoStreamEvents
func undoStreamEvents(handle C.uintptr_t, count C.size_t, paths **C.char, flags *C.FSEventStreamEventFlags) {
	onEvents := cgo.Handle(handle).Value().(func([]Event))
	pathList := unsafe.Slice(paths, count)
	flagList := unsafe.Slice(flags, count)
	events := make([]Event, count)
	for i := range events {
		events[i] = Event{Path: C.GoString(pathList[i]), Rescan: flagList[i]&rescanFlags != 0}
	}
	onEvents(events)
}
