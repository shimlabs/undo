package store

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// lock takes the store's exclusive lock and finishes any merge an earlier
// process left half done. A merge that fails again is logged rather than
// returned, so checkpoints keep working while State reports the problem.
func (s *Store) lock() (unlock func(), err error) {
	unlock, err = s.flock(unix.LOCK_EX)
	if err != nil {
		return nil, err
	}
	if err := s.finishMerge(); err != nil {
		s.LogError(fmt.Errorf("finishing the merge of old checkpoints: %w", err))
	}
	return unlock, nil
}

// RLock blocks changes to stored checkpoints until unlock is called. Taking a
// checkpoint or pruning waits for it, but other readers do not.
func (s *Store) RLock() (unlock func(), err error) {
	return s.flock(unix.LOCK_SH)
}

func (s *Store) flock(operation int) (unlock func(), err error) {
	file, err := s.openFile(s.lockPath(), os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(file.Fd()), operation); err != nil {
		file.Close()
		return nil, err
	}
	// Closing the file releases the lock.
	unlock = func() { file.Close() }
	if err := s.loadConfig(); err != nil {
		unlock()
		return nil, err
	}
	return unlock, nil
}
