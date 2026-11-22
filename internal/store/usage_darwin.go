package store

/*
#include <sys/attr.h>
#include <stdlib.h>
#include <sys/types.h>
#include <unistd.h>

struct undoPrivateSizeBuffer {
	u_int32_t length;
	off_t size;
} __attribute__((aligned(4), packed));

static long long undoPrivateSize(const char *path) {
	struct attrlist list = {0};
	list.bitmapcount = ATTR_BIT_MAP_COUNT;
	list.forkattr = ATTR_CMNEXT_PRIVATESIZE;
	struct undoPrivateSizeBuffer buffer;
	if (getattrlist(path, &list, &buffer, sizeof(buffer), FSOPT_ATTR_CMN_EXTENDED | FSOPT_NOFOLLOW) != 0) {
		return -1;
	}
	return buffer.size;
}
*/
import "C"

import (
	"os"
	"unsafe"
)

func privateSize(path string) (int64, error) {
	cPath := C.CString(path)
	defer C.free(unsafe.Pointer(cPath))
	size, err := C.undoPrivateSize(cPath)
	if size < 0 {
		return 0, &os.PathError{Op: "getattrlist", Path: path, Err: err}
	}
	return int64(size), nil
}
