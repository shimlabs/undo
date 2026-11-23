// Package walk visits directory trees with several directories in flight at
// once. Reading a directory mostly waits on the filesystem, so a large tree is
// walked several times faster in parallel.
package walk

import (
	"sync"
	"sync/atomic"
)

// Workers is a good number of directories to read at once. Reading a
// directory mostly waits on the filesystem rather than the CPU, so it is set
// well above typical core counts.
const Workers = 32

// Dirs calls visit for root and then for every subdirectory visit returns,
// with up to workers calls running at once. The walk stops at the first error,
// which Dirs returns.
func Dirs[T any](root T, workers int, visit func(dir T) (subdirs []T, err error)) error {
	w := &walker[T]{visit: visit, slots: make(chan struct{}, workers)}
	w.walk(root)
	w.pending.Wait()
	return w.err
}

type walker[T any] struct {
	visit   func(T) ([]T, error)
	slots   chan struct{}
	pending sync.WaitGroup
	failed  atomic.Bool
	once    sync.Once
	err     error
}

// walk visits dir, then its subdirectories, each on a new goroutine while a
// slot is free and on the current one otherwise.
func (w *walker[T]) walk(dir T) {
	if w.failed.Load() {
		return
	}
	subdirs, err := w.visit(dir)
	if err != nil {
		w.once.Do(func() {
			w.err = err
			w.failed.Store(true)
		})
		return
	}
	for _, subdir := range subdirs {
		select {
		case w.slots <- struct{}{}:
			w.pending.Go(func() {
				defer func() { <-w.slots }()
				w.walk(subdir)
			})
		default:
			w.walk(subdir)
		}
	}
}
