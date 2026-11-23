package store

import (
	"os"
	"path/filepath"
	"time"
)

// Limits bound how much history a store keeps.
type Limits struct {
	MaxAge   time.Duration
	MaxCount int
	MaxBytes int64
}

// DefaultLimits keeps a week of checkpoints, at most 2,000 of them, using at
// most 5 GB of disk that is not shared with the project.
func DefaultLimits() Limits {
	return Limits{MaxAge: 7 * 24 * time.Hour, MaxCount: 2000, MaxBytes: 5_000_000_000}
}

// pruneInterval is how often PruneIfDue prunes. Pruning measures the whole
// store, which is too slow to do after every checkpoint.
const pruneInterval = time.Hour

// PruneIfDue prunes the store if it has not been pruned for pruneInterval. It
// is cheap to call after every checkpoint.
func (s *Store) PruneIfDue(now time.Time, limits Limits) (int, error) {
	marker := filepath.Join(s.dir, "last-prune")
	if info, err := os.Stat(marker); err == nil && now.Sub(info.ModTime()) < pruneInterval {
		return 0, nil
	}
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		return 0, err
	}
	if err := os.Chtimes(marker, now, now); err != nil {
		return 0, err
	}
	return s.Prune(now, limits)
}

// Prune merges the oldest checkpoints into the base until the store is within
// limits. The latest checkpoint is always kept. It returns how many
// checkpoints were merged. The store is locked only while merging, so
// measuring disk use does not hold up new checkpoints.
func (s *Store) Prune(now time.Time, limits Limits) (int, error) {
	count, err := s.mergeExpired(now, limits)
	if err != nil {
		return count, err
	}
	usage, err := s.Usage()
	if err != nil || usage <= limits.MaxBytes {
		return count, err
	}
	bySize, err := s.mergeUntilUnder(limits.MaxBytes, usage)
	return count + bySize, err
}

func (s *Store) mergeExpired(now time.Time, limits Limits) (int, error) {
	unlock, err := s.lock()
	if err != nil {
		return 0, err
	}
	defer unlock()

	list, err := s.List()
	if err != nil {
		return 0, err
	}
	count := 0
	for count < len(list)-1 && (now.Sub(list[count].Time) > limits.MaxAge || len(list)-count > limits.MaxCount) {
		count++
	}
	if count == 0 {
		return 0, nil
	}
	_, err = s.merge(list[count-1].ID)
	return count, err
}

// mergeUntilUnder merges one checkpoint at a time and subtracts the bytes each
// merge frees from usage, since measuring the whole store after every merge
// would be slow.
func (s *Store) mergeUntilUnder(maxBytes, usage int64) (int, error) {
	unlock, err := s.lock()
	if err != nil {
		return 0, err
	}
	defer unlock()

	list, err := s.List()
	if err != nil {
		return 0, err
	}
	count := 0
	for ; usage > maxBytes && count < len(list)-1; count++ {
		freed, err := s.merge(list[count].ID)
		if err != nil {
			return count, err
		}
		usage -= freed
	}
	return count, nil
}
