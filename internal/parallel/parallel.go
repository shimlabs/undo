// Package parallel runs independent filesystem calls at the same time.
// Cloning a file is mostly waiting on the filesystem, so a few calls in
// flight finish much sooner than one after another.
package parallel

import (
	"sync"
	"sync/atomic"
)

// concurrency is how many calls run at once. More gave no further gain when
// cloning, and fewer leave the filesystem idle.
const concurrency = 8

// For calls fn with every index from 0 to n-1, several at a time. After the
// first error it starts no new calls, and it returns that error once the
// running calls are done.
func For(n int, fn func(i int) error) error {
	var (
		next      atomic.Int64
		hasFailed atomic.Bool
		once      sync.Once
		firstErr  error
		workers   sync.WaitGroup
	)
	for range min(concurrency, n) {
		workers.Go(func() {
			for !hasFailed.Load() {
				i := int(next.Add(1) - 1)
				if i >= n {
					return
				}
				if err := fn(i); err != nil {
					once.Do(func() { firstErr = err })
					hasFailed.Store(true)
				}
			}
		})
	}
	workers.Wait()
	return firstErr
}
