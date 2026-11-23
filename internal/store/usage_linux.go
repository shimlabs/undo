package store

import (
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

// The FIEMAP ioctl from linux/fiemap.h, which x/sys does not wrap.
const (
	fsIocFiemap        = 0xc020660b
	fiemapFlagSync     = 0x1
	fiemapExtentLast   = 0x1
	fiemapExtentShared = 0x2000
	fiemapBatch        = 128
)

type fiemapExtent struct {
	Logical    uint64
	Physical   uint64
	Length     uint64
	reserved64 [2]uint64
	Flags      uint32
	reserved   [3]uint32
}

type fiemapRequest struct {
	Start         uint64
	Length        uint64
	Flags         uint32
	MappedExtents uint32
	ExtentCount   uint32
	reserved      uint32
	Extents       [fiemapBatch]fiemapExtent
}

// privateSize returns the bytes of path in extents that no clone shares. Small
// files stored inline report a whole block, so the total is capped at the
// file's size.
func privateSize(path string) (int64, error) {
	// A file swapped for a named pipe since it was listed would block a
	// plain open.
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return 0, &os.PathError{Op: "open", Path: path, Err: err}
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return 0, err
	}
	total, err := unsharedBytes(file)
	return min(total, info.Size()), err
}

func unsharedBytes(file *os.File) (int64, error) {
	var total int64
	var request fiemapRequest
	for start := uint64(0); ; {
		request = fiemapRequest{Start: start, Length: ^uint64(0) - start, Flags: fiemapFlagSync, ExtentCount: fiemapBatch}
		_, _, errno := unix.Syscall(unix.SYS_IOCTL, file.Fd(), fsIocFiemap, uintptr(unsafe.Pointer(&request)))
		if errno != 0 {
			return 0, &os.PathError{Op: "fiemap", Path: file.Name(), Err: errno}
		}
		if request.MappedExtents == 0 {
			return total, nil
		}
		for _, extent := range request.Extents[:request.MappedExtents] {
			if extent.Flags&fiemapExtentShared == 0 {
				total += int64(extent.Length)
			}
			if extent.Flags&fiemapExtentLast != 0 {
				return total, nil
			}
			start = extent.Logical + extent.Length
		}
	}
}
