package watch

/*
#include <unistd.h>
#include <limits.h>
*/
import "C"

import (
	"net"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

// userRuntimeDir returns the user's private temporary directory. It asks the
// system instead of reading $TMPDIR, which agents do not always pass on to
// the hooks they run.
func userRuntimeDir() string {
	buffer := make([]byte, C.PATH_MAX)
	if C.confstr(C._CS_DARWIN_USER_TEMP_DIR, (*C.char)(unsafe.Pointer(&buffer[0])), C.size_t(len(buffer))) == 0 {
		return os.TempDir()
	}
	return unix.ByteSliceToString(buffer)
}

func peerUID(conn *net.UnixConn) (int, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return 0, err
	}
	var credentials *unix.Xucred
	var credentialsErr error
	if err := raw.Control(func(fd uintptr) {
		credentials, credentialsErr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	}); err != nil {
		return 0, err
	}
	if credentialsErr != nil {
		return 0, credentialsErr
	}
	return int(credentials.Uid), nil
}
