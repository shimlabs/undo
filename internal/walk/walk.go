// Package walk visits directory trees with several directories in flight at
// once. Reading a directory mostly waits on the filesystem, so a large tree is
// walked several times faster in parallel.
package walk

import (
	"sync"
	"sync/atomic"
)

// concurrency is how many directories are read at once. Reading a directory
// mostly waits on the filesystem rather than the CPU, so this is set well
// above typical core counts.
const concurrency = 32

// Dirs calls visit for root and then for every subdirectory visit returns,
// with several calls running at once. The walk stops at the first error,
// which Dirs returns.
func Dirs[T any](root T, visit func(dir T) (subdirs []T, err error)) error {
	w := &walker[T]{visit: visit, semaphore: make(chan struct{}, concurrency)}
	w.walk(root)
	w.pending.Wait()
	return w.err
}

type walker[T any] struct {
	visit     func(T) ([]T, error)
	semaphore chan struct{}
	pending   sync.WaitGroup
	hasFailed atomic.Bool
	once      sync.Once
	err       error
}

// walk visits dir, then its subdirectories, each on a new goroutine while the
// semaphore allows and on the current one otherwise.
func (w *walker[T]) walk(dir T) {
	if w.hasFailed.Load() {
		return
	}
	subdirs, err := w.visit(dir)
	if err != nil {
		w.once.Do(func() {
			w.err = err
			w.hasFailed.Store(true)
		})
		return
	}
	for _, subdir := range subdirs {
		select {
		case w.semaphore <- struct{}{}:
			w.pending.Go(func() {
				defer func() { <-w.semaphore }()
				w.walk(subdir)
			})
		default:
			w.walk(subdir)
		}
	}
}
