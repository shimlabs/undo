package project

/*
#include <fcntl.h>
#include <sys/param.h>

static int undoGetPath(int fd, char *buf) {
	return fcntl(fd, F_GETPATH, buf);
}
*/
import "C"

import (
	"os"
	"unsafe"
)

// canonicalCase returns path spelled the way the filesystem stores it. APFS
// is usually case-insensitive, so a path typed in another case reaches the
// same folder, but file events report the stored spelling, and the watcher
// would ignore them.
func canonicalCase(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	buf := make([]byte, C.MAXPATHLEN)
	if status, err := C.undoGetPath(C.int(file.Fd()), (*C.char)(unsafe.Pointer(&buf[0]))); status != 0 {
		return "", &os.PathError{Op: "fcntl", Path: path, Err: err}
	}
	return C.GoString((*C.char)(unsafe.Pointer(&buf[0]))), nil
}
