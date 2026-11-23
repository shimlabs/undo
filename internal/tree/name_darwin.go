package tree

/*
#include <stdlib.h>
#include <string.h>
#include <sys/attr.h>
#include <sys/syslimits.h>
#include <unistd.h>

struct undoNameBuffer {
	u_int32_t length;
	attrreference_t name;
	char data[NAME_MAX * 3 + 1];
} __attribute__((aligned(4), packed));

static int undoStoredName(const char *path, char *name, size_t size) {
	struct attrlist list;
	memset(&list, 0, sizeof(list));
	list.bitmapcount = ATTR_BIT_MAP_COUNT;
	list.commonattr = ATTR_CMN_NAME;
	struct undoNameBuffer buffer;
	if (getattrlist(path, &list, &buffer, sizeof(buffer), FSOPT_NOFOLLOW) != 0) {
		return -1;
	}
	const char *start = (const char *)&buffer.name + buffer.name.attr_dataoffset;
	strlcpy(name, start, size);
	return 0;
}
*/
import "C"

import (
	"os"
	"unsafe"
)

// StoredName returns the name of the last element of path as the directory
// stores it. APFS treats names that differ only in case or Unicode
// normalization as the same, so a path can reach a file under a spelling
// that is not its own.
func StoredName(path string) (string, error) {
	cPath := C.CString(path)
	defer C.free(unsafe.Pointer(cPath))
	var name [C.NAME_MAX*3 + 1]C.char
	if status, err := C.undoStoredName(cPath, &name[0], C.size_t(len(name))); status != 0 {
		return "", &os.PathError{Op: "getattrlist", Path: path, Err: err}
	}
	return C.GoString(&name[0]), nil
}
